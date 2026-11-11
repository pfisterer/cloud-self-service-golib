package credential_test

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pfisterer/cloud-self-service-golib/credential"
	"github.com/pfisterer/cloud-self-service-golib/oidcauth"
	"github.com/pfisterer/cloud-self-service-golib/token"
	"go.uber.org/zap"
)

const (
	issuer   = "https://sso.example/realms/test"
	clientID = "test-client"
	prefix   = "svc_"
)

// provider is a key set served over HTTP and a key to sign ID tokens with.
type provider struct {
	key  *rsa.PrivateKey
	url  string
	down bool
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &provider{key: key}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if p.down {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		b64 := base64.RawURLEncoding.EncodeToString
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(srv.Close)
	p.url = srv.URL
	return p
}

// sign makes an RS256 ID token with these claims on top of a valid issuer, audience and expiry.
func (p *provider) sign(t *testing.T, claims map[string]any) string {
	t.Helper()
	all := map[string]any{"iss": issuer, "aud": clientID, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}
	for k, v := range claims {
		all[k] = v
	}
	part := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	signing := part(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"}) + "." + part(all)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (p *provider) verifier(t *testing.T) *oidcauth.Verifier {
	t.Helper()
	v, err := oidcauth.New(oidcauth.Config{IssuerURL: issuer, ClientID: clientID, JWKSURL: p.url}, zap.NewNop().Sugar())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// brokenStore fails every lookup the way a database that is away does.
type brokenStore struct{ token.Store }

func (brokenStore) ByHash(context.Context, string) (token.Record, error) {
	return token.Record{}, errors.New("connection refused")
}

func TestResolve(t *testing.T) {
	ctx := context.Background()
	p := newProvider(t)
	v := p.verifier(t)
	tokens := token.NewService(prefix, token.NewMemoryStore())
	rw, err := tokens.Issue(ctx, "Anna@Example.org", token.IssueOptions{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ro, err := tokens.Issue(ctx, "ben@example.org", token.IssueOptions{TTL: time.Hour, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	expired, err := token.NewService(prefix, token.NewMemoryStore()).WithClock(func() time.Time { return time.Now().Add(-2 * time.Hour) }).Issue(ctx, "x@example.org", token.IssueOptions{TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	broken := token.NewService(prefix, brokenStore{})
	good := p.sign(t, map[string]any{"sub": "s-1", "email": "Carla@Example.org"})
	otherAudience := p.sign(t, map[string]any{"sub": "s-1", "aud": "another-client"})
	stale := p.sign(t, map[string]any{"sub": "s-1", "exp": time.Now().Add(-time.Hour).Unix()})

	down := newProvider(t)
	down.down = true
	downVerifier := down.verifier(t)

	var nilService *token.Service
	var nilVerifier *oidcauth.Verifier

	cases := []struct {
		name     string
		header   string
		tokens   credential.Tokens
		verifier credential.Verifier
		wantErr  error
		// for a success
		identity string
		viaToken bool
		readOnly bool
	}{
		{name: "no header", header: "", tokens: tokens, verifier: v, wantErr: credential.ErrMissing},
		{name: "another scheme", header: "Basic dXNlcjpwYXNz", tokens: tokens, verifier: v, wantErr: credential.ErrMissing},
		{name: "scheme without credential", header: "Bearer", tokens: tokens, verifier: v, wantErr: credential.ErrMissing},
		{name: "empty bearer", header: "Bearer   ", tokens: tokens, verifier: v, wantErr: credential.ErrMissing},
		{name: "api token", header: "Bearer " + rw.Secret, tokens: tokens, verifier: v, identity: "anna@example.org", viaToken: true},
		{name: "api token, lower-case scheme", header: "bearer " + rw.Secret, tokens: tokens, verifier: v, identity: "anna@example.org", viaToken: true},
		{name: "read-only api token", header: "Bearer " + ro.Secret, tokens: tokens, verifier: v, identity: "ben@example.org", viaToken: true, readOnly: true},
		{name: "api token without a verifier", header: "Bearer " + rw.Secret, tokens: tokens, verifier: nil, identity: "anna@example.org", viaToken: true},
		{name: "unknown api token", header: "Bearer " + prefix + "nonsense", tokens: tokens, verifier: v, wantErr: credential.ErrRejected},
		{name: "expired api token", header: "Bearer " + expired.Secret, tokens: tokens, verifier: v, wantErr: credential.ErrRejected},
		{name: "token store away", header: "Bearer " + prefix + "anything", tokens: broken, verifier: v, wantErr: errStore},
		{name: "id token", header: "Bearer " + good, tokens: tokens, verifier: v, identity: "carla@example.org"},
		{name: "id token, no token service", header: "Bearer " + good, tokens: nil, verifier: v, identity: "carla@example.org"},
		{name: "id token, nil *token.Service", header: "Bearer " + good, tokens: nilService, verifier: v, identity: "carla@example.org"},
		{name: "id token for another audience", header: "Bearer " + otherAudience, tokens: tokens, verifier: v, wantErr: credential.ErrRejected},
		{name: "expired id token", header: "Bearer " + stale, tokens: tokens, verifier: v, wantErr: credential.ErrRejected},
		{name: "garbage bearer", header: "Bearer not-a-jwt", tokens: tokens, verifier: v, wantErr: credential.ErrRejected},
		{name: "id token, provider away", header: "Bearer " + good, tokens: tokens, verifier: downVerifier, wantErr: credential.ErrKeysUnavailable},
		{name: "id token, no verifier", header: "Bearer " + good, tokens: tokens, verifier: nil, wantErr: credential.ErrNoVerifier},
		{name: "id token, nil *oidcauth.Verifier", header: "Bearer " + good, tokens: tokens, verifier: nilVerifier, wantErr: credential.ErrNoVerifier},
		// The prefix routes: a token with it never reaches the provider, even when the store does not know it.
		{name: "prefixed bearer never goes to the provider", header: "Bearer " + prefix + good, tokens: tokens, verifier: v, wantErr: credential.ErrRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := credential.Resolve(ctx, tc.header, tc.tokens, tc.verifier)
			switch {
			case tc.wantErr == errStore:
				if err == nil || errors.Is(err, credential.ErrRejected) || errors.Is(err, credential.ErrMissing) || errors.Is(err, credential.ErrKeysUnavailable) {
					t.Fatalf("a store failure must be its own error (500), got %v", err)
				}
				return
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				for _, other := range []error{credential.ErrMissing, credential.ErrRejected, credential.ErrKeysUnavailable, credential.ErrNoVerifier} {
					if other != tc.wantErr && errors.Is(err, other) {
						t.Fatalf("err = %v is also %v", err, other)
					}
				}
				if tc.wantErr == credential.ErrRejected && (credential.Cause(err) == nil || errors.Is(credential.Cause(err), credential.ErrRejected)) {
					t.Fatalf("Cause(%v) = %v, want what the rejection was made from", err, credential.Cause(err))
				}
				if got != (credential.Principal{}) {
					t.Fatalf("a failure returned a principal: %+v", got)
				}
				return
			case err != nil:
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Identity() != tc.identity || got.ViaAPIToken() != tc.viaToken || got.ReadOnly != tc.readOnly {
				t.Fatalf("got identity %q, via token %v, read-only %v; want %q, %v, %v", got.Identity(), got.ViaAPIToken(), got.ReadOnly, tc.identity, tc.viaToken, tc.readOnly)
			}
			if tc.viaToken && (got.Token == nil || got.Claims.Email != got.Token.Subject) {
				t.Fatalf("an api token's claims must carry its subject: %+v", got)
			}
			if !tc.viaToken && (got.Token != nil || got.Claims.Subject != "s-1") {
				t.Fatalf("an id token's claims must be the verified ones: %+v", got)
			}
		})
	}
}

func TestCause(t *testing.T) {
	other := errors.New("x")
	if credential.Cause(other) != other {
		t.Fatal("Cause of an error that is no rejection is the error")
	}
}

// errStore marks the case where the store fails: an error of its own, none of the sentinels.
var errStore = errors.New("store")
