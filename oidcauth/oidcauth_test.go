package oidcauth_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pfisterer/cloud-self-service-golib/oidcauth"
	"go.uber.org/zap"
)

// tokenFor builds a syntactically valid token with the right issuer, audience
// and expiry, and a signature that is nonsense. go-oidc checks those claims
// before it fetches a key, so anything malformed never reaches the provider —
// and it is the fetch that these tests are about.
func tokenFor(issuer, clientID string) string {
	part := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	header := part(map[string]string{"alg": "RS256", "kid": "test-key"})
	payload := part(map[string]any{
		"iss": issuer,
		"aud": clientID,
		"sub": "someone",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	return fmt.Sprintf("%s.%s.%s", header, payload, base64.RawURLEncoding.EncodeToString([]byte("not-a-signature")))
}

const testIssuer = "https://sso.example/realms/test"
const testClientID = "test-client"

func newVerifier(t *testing.T, jwksURL string) *oidcauth.Verifier {
	t.Helper()
	v, err := oidcauth.New(oidcauth.Config{
		IssuerURL: testIssuer,
		ClientID:  testClientID,
		JWKSURL:   jwksURL,
	}, zap.NewNop().Sugar())
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	return v
}

// The whole point of the package: with the key set configured, starting up asks
// the provider nothing — so a provider that is down cannot stop a service from
// starting.
func TestNew_DoesNotTouchTheProvider(t *testing.T) {
	// A port nothing listens on. Any network call here fails immediately.
	v := newVerifier(t, "http://127.0.0.1:1/certs")
	if v.KeysUnavailable() {
		t.Fatal("a provider nobody has asked anything of is not known to be broken")
	}
}

// A provider that is away must be told apart from a token we reject: the caller
// answers 503 for one and 401 for the other, and the difference decides whether
// the browser throws its session away.
func TestVerify_ProviderDown(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	defer server.Close()

	v := newVerifier(t, server.URL)
	_, err := v.Verify(context.Background(), tokenFor(testIssuer, testClientID))
	if !errors.Is(err, oidcauth.ErrKeysUnavailable) {
		t.Fatalf("a 502 from the key set should read as unavailable, got %v", err)
	}
	if hits == 0 {
		t.Fatal("the key set was never asked")
	}
	if !v.KeysUnavailable() {
		t.Fatal("the verifier should report itself degraded")
	}
	if v.LastKeyFetchError() == "" {
		t.Fatal("a degraded verifier should say what happened")
	}
}

// Keys that arrive fine and a token that does not verify is an ordinary
// rejection — 401 — even though Verify fails in both cases.
func TestVerify_BadTokenWithHealthyProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer server.Close()

	v := newVerifier(t, server.URL)
	_, err := v.Verify(context.Background(), tokenFor(testIssuer, testClientID))
	if err == nil {
		t.Fatal("a token signed by nobody must not verify")
	}
	if errors.Is(err, oidcauth.ErrKeysUnavailable) {
		t.Fatalf("the provider answered, so this is a bad token, not an outage: %v", err)
	}
	if v.KeysUnavailable() {
		t.Fatal("a provider that answered is not degraded")
	}
}

// A network error counts the same as a 502 — both mean "cannot judge this now".
func TestVerify_ProviderUnreachable(t *testing.T) {
	v := newVerifier(t, "http://127.0.0.1:1/certs")
	_, err := v.Verify(context.Background(), tokenFor(testIssuer, testClientID))
	if !errors.Is(err, oidcauth.ErrKeysUnavailable) {
		t.Fatalf("an unreachable key set should read as unavailable, got %v", err)
	}
}

// The degraded state describes the present, not the past: once the window has
// passed the verifier stops claiming the provider is away, so a status page
// does not keep showing an outage that ended.
func TestKeysUnavailable_Expires(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	v, err := oidcauth.New(oidcauth.Config{
		IssuerURL:        testIssuer,
		ClientID:         testClientID,
		JWKSURL:          server.URL,
		KeyFailureWindow: 50 * time.Millisecond,
	}, zap.NewNop().Sugar())
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	_, _ = v.Verify(context.Background(), tokenFor(testIssuer, testClientID))
	if !v.KeysUnavailable() {
		t.Fatal("setup: the verifier should be degraded right after the failure")
	}
	time.Sleep(80 * time.Millisecond)
	if v.KeysUnavailable() {
		t.Fatal("the failure is older than the window and no longer describes the present")
	}
}

// Without a key set address the verifier falls back to discovery, which needs
// the provider right now — the old behaviour, kept for local and mock setups.
func TestNew_DiscoveryStillWorks(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"` + server.URL + `","jwks_uri":"` + server.URL + `/certs"}`))
	}))
	defer server.Close()

	if _, err := oidcauth.New(oidcauth.Config{IssuerURL: server.URL, ClientID: "c"}, zap.NewNop().Sugar()); err != nil {
		t.Fatalf("discovery against a healthy provider should work: %v", err)
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer down.Close()
	if _, err := oidcauth.New(oidcauth.Config{IssuerURL: down.URL, ClientID: "c"}, zap.NewNop().Sugar()); err == nil {
		t.Fatal("discovery against a dead provider must fail — that is why JWKSURL exists")
	}
}

func TestNew_RequiresIssuer(t *testing.T) {
	if _, err := oidcauth.New(oidcauth.Config{ClientID: "c", JWKSURL: "http://x/certs"}, zap.NewNop().Sugar()); err == nil {
		t.Fatal("the issuer is what a token's iss claim is checked against; it cannot be empty")
	}
}
