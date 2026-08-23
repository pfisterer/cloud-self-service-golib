package authn

import (
	"encoding/json"
	"testing"
)

func TestClaimsIdentity(t *testing.T) {
	cases := []struct {
		name   string
		claims *Claims
		want   string
	}{
		{name: "nil claims", claims: nil, want: ""},
		{name: "empty claims", claims: &Claims{}, want: ""},
		{
			name:   "email wins",
			claims: &Claims{Subject: "s1", Email: "alice@example.edu", PreferredUsername: "alice"},
			want:   "alice@example.edu",
		},
		{
			name:   "preferred_username when no email",
			claims: &Claims{Subject: "s1", PreferredUsername: "alice"},
			want:   "alice",
		},
		{
			name:   "sub is the last resort",
			claims: &Claims{Subject: "s1"},
			want:   "s1",
		},
		{
			name:   "blank claims are skipped, not returned",
			claims: &Claims{Subject: "s1", Email: "   "},
			want:   "s1",
		},
		{
			name:   "the answer is trimmed",
			claims: &Claims{Email: "  alice@example.edu  "},
			want:   "alice@example.edu",
		},
		// Case is preserved on purpose: this value is already on disk as the
		// owner of zones and of API tokens. Folding it here would stop matching
		// what is stored.
		{
			name:   "case is preserved",
			claims: &Claims{Email: "Alice@Example.EDU"},
			want:   "Alice@Example.EDU",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.claims.Identity(); got != tc.want {
				t.Errorf("Identity() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The struct is unmarshalled straight from a verified ID token, so the claim
// names are part of its contract rather than an implementation detail.
func TestClaimsUnmarshalStandardClaimNames(t *testing.T) {
	const token = `{
		"sub": "8f14e45f",
		"email": "alice@example.edu",
		"preferred_username": "alice",
		"name": "Alice Example",
		"aud": "ignored"
	}`

	var claims Claims
	if err := json.Unmarshal([]byte(token), &claims); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	want := Claims{
		Subject:           "8f14e45f",
		Email:             "alice@example.edu",
		PreferredUsername: "alice",
		Name:              "Alice Example",
	}
	if claims != want {
		t.Errorf("got %+v, want %+v", claims, want)
	}
}

func TestCutBearerPrefix(t *testing.T) {
	cases := []struct {
		name      string
		header    string
		wantToken string
		wantOK    bool
	}{
		{name: "canonical", header: "Bearer abc123", wantToken: "abc123", wantOK: true},
		// RFC 7235 makes the scheme case-insensitive. Rejecting this was a real
		// failure and is the reason the helper exists rather than a TrimPrefix.
		{name: "lowercase scheme", header: "bearer abc123", wantToken: "abc123", wantOK: true},
		{name: "mixed case scheme", header: "BeArEr abc123", wantToken: "abc123", wantOK: true},
		{name: "surrounding space in the credential", header: "Bearer   abc123  ", wantToken: "abc123", wantOK: true},
		{name: "empty header", header: "", wantOK: false},
		{name: "another scheme", header: "Basic abc123", wantOK: false},
		{name: "credential without a scheme", header: "abc123", wantOK: false},
		{name: "shorter than the prefix", header: "Bear", wantOK: false},
		// The scheme is present but the credential is not: the caller has to be
		// able to tell this from "no Bearer header at all", because it is the
		// difference between a broken client and an unauthenticated one.
		{name: "scheme without a credential", header: "Bearer ", wantToken: "", wantOK: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, ok := CutBearerPrefix(tc.header)
			if ok != tc.wantOK {
				t.Fatalf("CutBearerPrefix(%q) ok = %v, want %v", tc.header, ok, tc.wantOK)
			}
			if ok && token != tc.wantToken {
				t.Errorf("CutBearerPrefix(%q) = %q, want %q", tc.header, token, tc.wantToken)
			}
		})
	}
}

func TestScheme(t *testing.T) {
	cases := []struct{ header, want string }{
		{header: "Bearer abc123", want: "Bearer"},
		{header: "Basic dXNlcjpwdw==", want: "Basic"},
		{header: "  Token abc123", want: "Token"},
		{header: "", want: ""},
		{header: "abc123", want: "abc123"},
	}

	for _, tc := range cases {
		t.Run(tc.header, func(t *testing.T) {
			if got := Scheme(tc.header); got != tc.want {
				t.Errorf("Scheme(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

// The whole point of Scheme is that it is safe to log. A header whose scheme is
// missing degenerates to the value itself — which for "abc123" above is the
// credential. That is acceptable only because a credential sent with no scheme
// at all is not a working one, but it is worth pinning down so nobody widens
// the function into something that leaks.
func TestSchemeNeverReturnsTheCredentialOfAWellFormedHeader(t *testing.T) {
	const credential = "dynz_token_deadbeefdeadbeef"

	for _, header := range []string{
		"Bearer " + credential,
		"bearer " + credential,
		"Basic " + credential,
		"Token " + credential,
	} {
		if got := Scheme(header); got == credential {
			t.Errorf("Scheme(%q) returned the credential", header)
		}
	}
}
