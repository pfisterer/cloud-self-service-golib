package oidcauth_test

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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pfisterer/cloud-self-service-golib/authn"
	"github.com/pfisterer/cloud-self-service-golib/oidcauth"
	"github.com/pfisterer/cloud-self-service-golib/retry"
	"go.uber.org/zap"
)

// keyServer serves a key set over HTTP that a test can change, and counts how often it is asked.
type keyServer struct {
	url  string
	hits atomic.Int64

	mu   sync.Mutex
	keys map[string]*rsa.PrivateKey
	down bool
}

func newKeyServer(t *testing.T) *keyServer {
	t.Helper()
	ks := &keyServer{keys: map[string]*rsa.PrivateKey{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ks.hits.Add(1)
		ks.mu.Lock()
		defer ks.mu.Unlock()
		if ks.down {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		b64 := base64.RawURLEncoding.EncodeToString
		var keys []map[string]string
		for kid, key := range ks.keys {
			keys = append(keys, map[string]string{"kty": "RSA", "kid": kid, "alg": "RS256", "use": "sig", "n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes())})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
	t.Cleanup(srv.Close)
	ks.url = srv.URL
	return ks
}

// addKey publishes a new signing key under kid and returns it.
func (ks *keyServer) addKey(t *testing.T, kid string) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ks.mu.Lock()
	ks.keys[kid] = key
	ks.mu.Unlock()
	return key
}

func (ks *keyServer) setDown(down bool) {
	ks.mu.Lock()
	ks.down = down
	ks.mu.Unlock()
}

// signed makes an RS256 ID token with a valid issuer, audience and expiry, plus these claims.
func signed(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	all := map[string]any{"iss": testIssuer, "aud": testClientID, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "sub": "s-1"}
	for k, v := range claims {
		all[k] = v
	}
	part := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	signing := part(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"}) + "." + part(all)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func verifierFor(t *testing.T, jwksURL string, minRefetch time.Duration) *oidcauth.Verifier {
	t.Helper()
	v, err := oidcauth.New(oidcauth.Config{IssuerURL: testIssuer, ClientID: testClientID, JWKSURL: jwksURL, MinRefetchInterval: minRefetch}, zap.NewNop().Sugar())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Verify is where tokens enter, so the identity rules hold there for every service, including one that reads claims.Email itself.
func TestVerify_Identity(t *testing.T) {
	ks := newKeyServer(t)
	key := ks.addKey(t, "k1")
	v := verifierFor(t, ks.url, 0)

	cases := []struct {
		name   string
		claims map[string]any
		want   string
		err    error
	}{
		{name: "verified address", claims: map[string]any{"email": "Alice@Example.org", "email_verified": true}, want: "alice@example.org"},
		{name: "unverified address", claims: map[string]any{"email": "alice@example.org", "email_verified": false}, err: authn.ErrEmailNotVerified},
		{name: "email_verified missing", claims: map[string]any{"email": "alice@example.org"}, err: authn.ErrEmailNotVerified},
		{name: "no address", claims: map[string]any{"preferred_username": "alice@example.org"}, err: authn.ErrNoEmail},
		{name: "service account", claims: map[string]any{"preferred_username": "service-account-lti", "email_verified": false}, err: authn.ErrNoEmail},
		{name: "non-ASCII address", claims: map[string]any{"email": "jürgen@example.org", "email_verified": true}, err: authn.ErrInvalidEmail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims, err := v.Verify(context.Background(), signed(t, key, "k1", tc.claims))
			if tc.err != nil {
				if !errors.Is(err, oidcauth.ErrNoIdentity) || !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want ErrNoIdentity wrapping %v", err, tc.err)
				}
				if errors.Is(err, oidcauth.ErrKeysUnavailable) {
					t.Fatal("a token without an identity is rejected (401), not unjudgeable (503)")
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if claims.Identity() != tc.want {
				t.Fatalf("Identity() = %q, want %q", claims.Identity(), tc.want)
			}
		})
	}
}

func TestNew_RequiresClientID(t *testing.T) {
	for _, id := range []string{"", "   "} {
		if _, err := oidcauth.New(oidcauth.Config{IssuerURL: testIssuer, ClientID: id, JWKSURL: "http://127.0.0.1:1/certs"}, zap.NewNop().Sugar()); err == nil {
			t.Fatalf("ClientID %q: without an audience to check, a token for any client of the realm would do", id)
		}
	}
	if _, err := oidcauth.New(oidcauth.Config{IssuerURL: "  ", ClientID: testClientID, JWKSURL: "http://127.0.0.1:1/certs"}, zap.NewNop().Sugar()); err == nil {
		t.Fatal("a blank issuer is no issuer")
	}
	var retries int
	if _, err := oidcauth.NewWithRetry(context.Background(), oidcauth.Config{IssuerURL: testIssuer}, zap.NewNop().Sugar(), retry.Policy{Initial: time.Millisecond, OnRetry: func(int, error, time.Duration) { retries++ }}); err == nil || retries != 0 {
		t.Fatalf("a missing client ID: err %v after %d retries, want an error at once", err, retries)
	}
}

// Forged tokens with unknown kids must not turn into one request to the provider each: within MinRefetchInterval the last answer is used again.
func TestVerify_RefetchSpacing(t *testing.T) {
	const interval = 300 * time.Millisecond
	ks := newKeyServer(t)
	key := ks.addKey(t, "k1")
	v := verifierFor(t, ks.url, interval)
	ctx := context.Background()
	good := signed(t, key, "k1", map[string]any{"email": "alice@example.org", "email_verified": true})

	if _, err := v.Verify(ctx, good); err != nil {
		t.Fatalf("first token: %v", err)
	}
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("hits after the first token = %d, want 1", got)
	}

	// Twenty tokens signed by a key nobody published, each with its own kid.
	stranger, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		forged := signed(t, stranger, "unknown-"+string(rune('a'+i)), map[string]any{"email": "alice@example.org", "email_verified": true})
		if _, err := v.Verify(ctx, forged); err == nil {
			t.Fatal("a token signed by an unpublished key must not verify")
		} else if errors.Is(err, oidcauth.ErrKeysUnavailable) {
			t.Fatalf("the provider answered, so this is a bad token, not an outage: %v", err)
		}
	}
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("hits after 20 forged tokens within the interval = %d, want still 1", got)
	}
	// The cached key keeps working meanwhile.
	if _, err := v.Verify(ctx, good); err != nil {
		t.Fatalf("a good token while throttled: %v", err)
	}

	// A rotated key is picked up, but only once the interval has passed.
	rotated := ks.addKey(t, "k2")
	fresh := signed(t, rotated, "k2", map[string]any{"email": "alice@example.org", "email_verified": true})
	if _, err := v.Verify(ctx, fresh); err == nil {
		t.Fatal("within the interval the new key is not fetched yet")
	}
	time.Sleep(interval + 50*time.Millisecond)
	if _, err := v.Verify(ctx, fresh); err != nil {
		t.Fatalf("after the interval the new key is fetched: %v", err)
	}
	if got := ks.hits.Load(); got != 2 {
		t.Fatalf("hits after the interval = %d, want 2", got)
	}
}

// A provider that is away is not asked again on every token either, and still reads as away.
func TestVerify_RefetchSpacingWhileDown(t *testing.T) {
	ks := newKeyServer(t)
	key := ks.addKey(t, "k1")
	ks.setDown(true)
	v := verifierFor(t, ks.url, time.Minute)
	tok := signed(t, key, "k1", map[string]any{"email": "alice@example.org", "email_verified": true})
	for range 5 {
		if _, err := v.Verify(context.Background(), tok); !errors.Is(err, oidcauth.ErrKeysUnavailable) {
			t.Fatalf("err = %v, want ErrKeysUnavailable", err)
		}
	}
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1", got)
	}
}

// Concurrent tokens with unknown kids share one fetch.
func TestVerify_RefetchSpacingConcurrent(t *testing.T) {
	ks := newKeyServer(t)
	ks.addKey(t, "k1")
	v := verifierFor(t, ks.url, time.Minute)
	stranger, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tokens := make([]string, 20)
	for i := range tokens {
		tokens[i] = signed(t, stranger, "unknown-"+string(rune('a'+i)), map[string]any{"email": "alice@example.org", "email_verified": true})
	}
	var wg sync.WaitGroup
	for _, tok := range tokens {
		wg.Go(func() { _, _ = v.Verify(context.Background(), tok) })
	}
	wg.Wait()
	if got := ks.hits.Load(); got != 1 {
		t.Fatalf("hits = %d, want 1", got)
	}
}
