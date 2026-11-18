package authn

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// fromToken decodes claims the way oidcauth.Verify does, so Identity treats them as a provider's assertion.
func fromToken(t *testing.T, raw string) *Claims {
	t.Helper()
	var c Claims
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("Unmarshal(%s): %v", raw, err)
	}
	return &c
}

// Claims decoded from a token: the address counts only when verified, and nothing else stands in for it.
func TestClaimsIdentityFromToken(t *testing.T) {
	cases := []struct {
		name  string
		token string
		want  string
		err   error
	}{
		{name: "verified address", token: `{"sub":"s1","email":"alice@example.edu","email_verified":true}`, want: "alice@example.edu"},
		{name: "verified as a string", token: `{"sub":"s1","email":"alice@example.edu","email_verified":"true"}`, want: "alice@example.edu"},
		{name: "verified address is lowercased and trimmed", token: `{"email":"  Alice@Example.EDU ","email_verified":true}`, want: "alice@example.edu"},
		{name: "unverified address", token: `{"sub":"s1","email":"alice@example.edu","email_verified":false}`, err: ErrEmailNotVerified},
		{name: "email_verified missing", token: `{"sub":"s1","email":"alice@example.edu"}`, err: ErrEmailNotVerified},
		{name: "email_verified null", token: `{"email":"alice@example.edu","email_verified":null}`, err: ErrEmailNotVerified},
		{name: "email_verified is some other string", token: `{"email":"alice@example.edu","email_verified":"yes"}`, err: ErrEmailNotVerified},
		{name: "email_verified is a number", token: `{"email":"alice@example.edu","email_verified":1}`, err: ErrEmailNotVerified},
		// The old fallbacks: a username or sub that looks like somebody's address must not become that person.
		{name: "no address, username looks like an address", token: `{"sub":"s1","preferred_username":"bob@dhbw.de"}`, err: ErrNoEmail},
		{name: "no address, plain username", token: `{"sub":"s1","preferred_username":"alice"}`, err: ErrNoEmail},
		{name: "only sub", token: `{"sub":"bob@dhbw.de"}`, err: ErrNoEmail},
		{name: "blank address", token: `{"sub":"s1","email":"   ","email_verified":true}`, err: ErrNoEmail},
		{name: "no claims at all", token: `{}`, err: ErrNoEmail},
		// A Keycloak client-credentials token: no address, preferred_username service-account-<client>.
		{name: "service account", token: `{"sub":"5f0c","preferred_username":"service-account-role-provider","email_verified":false}`, err: ErrNoEmail},
		{name: "non-ASCII address, verified", token: `{"email":"jürgen@example.edu","email_verified":true}`, err: ErrInvalidEmail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fromToken(t, tc.token)
			got, err := c.CheckIdentity()
			if !errors.Is(err, tc.err) {
				t.Fatalf("CheckIdentity() err = %v, want %v", err, tc.err)
			}
			if got != tc.want || c.Identity() != tc.want {
				t.Errorf("CheckIdentity() = %q, Identity() = %q, want %q", got, c.Identity(), tc.want)
			}
		})
	}
}

// Claims built in code are the service's own word (an API token's subject, the X-Act-As target, the dev user): no email_verified needed, the address rules still hold, and there is still no fallback.
func TestClaimsIdentityBuiltInCode(t *testing.T) {
	cases := []struct {
		name   string
		claims *Claims
		want   string
		err    error
	}{
		{name: "nil claims", claims: nil, err: ErrNoEmail},
		{name: "empty claims", claims: &Claims{}, err: ErrNoEmail},
		{name: "address", claims: &Claims{Subject: "s1", Email: "alice@example.edu", PreferredUsername: "alice"}, want: "alice@example.edu"},
		{name: "an address is lowercased", claims: &Claims{Email: "Alice@Example.EDU"}, want: "alice@example.edu"},
		{name: "the answer is trimmed", claims: &Claims{Email: "  alice@example.edu  "}, want: "alice@example.edu"},
		{name: "a service identity without a dot in the domain", claims: &Claims{Email: "alertmanager@platform"}, want: "alertmanager@platform"},
		{name: "no fallback to preferred_username", claims: &Claims{Subject: "s1", PreferredUsername: "alice"}, err: ErrNoEmail},
		{name: "no fallback to sub", claims: &Claims{Subject: "AbC-123"}, err: ErrNoEmail},
		{name: "non-ASCII", claims: &Claims{Email: "jürgen@example.edu"}, err: ErrInvalidEmail},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.claims.CheckIdentity()
			if !errors.Is(err, tc.err) {
				t.Fatalf("CheckIdentity() err = %v, want %v", err, tc.err)
			}
			if got != tc.want || tc.claims.Identity() != tc.want {
				t.Errorf("CheckIdentity() = %q, Identity() = %q, want %q", got, tc.claims.Identity(), tc.want)
			}
		})
	}
}

func TestCanonicalEmail(t *testing.T) {
	cases := []struct {
		in, want string
		err      error
	}{
		{in: "alice@example.edu", want: "alice@example.edu"},
		{in: " Alice.B+tag@Sub.Example.EDU ", want: "alice.b+tag@sub.example.edu"},
		{in: "o'brien@example.edu", want: "o'brien@example.edu"},
		{in: "alertmanager@platform", want: "alertmanager@platform"},
		{in: "", err: ErrNoEmail},
		{in: "  ", err: ErrNoEmail},
		{in: "alice", err: ErrInvalidEmail},
		{in: "@example.edu", err: ErrInvalidEmail},
		{in: "alice@", err: ErrInvalidEmail},
		{in: "a@b@example.edu", err: ErrInvalidEmail},
		{in: "a b@example.edu", err: ErrInvalidEmail},
		{in: "a@x.de,b@x.de", err: ErrInvalidEmail},
		{in: "a@x.de;b@x.de", err: ErrInvalidEmail},
		{in: "Alice <alice@example.edu>", err: ErrInvalidEmail},
		{in: `"a b"@example.edu`, err: ErrInvalidEmail},
		{in: "a@[127.0.0.1]", err: ErrInvalidEmail},
		{in: ".alice@example.edu", err: ErrInvalidEmail},
		{in: "alice.@example.edu", err: ErrInvalidEmail},
		{in: "al..ice@example.edu", err: ErrInvalidEmail},
		{in: "alice@example..edu", err: ErrInvalidEmail},
		{in: "alice@.example.edu", err: ErrInvalidEmail},
		{in: "alice@example.edu.", err: ErrInvalidEmail},
		{in: "alice\t@example.edu", err: ErrInvalidEmail},
		{in: "alice\x00@example.edu", err: ErrInvalidEmail},
		{in: "alice\x7f@example.edu", err: ErrInvalidEmail},
		// Non-ASCII is refused, never folded: the Kelvin sign lowercases to an ASCII "k", the Cyrillic "а" looks like a Latin "a".
		{in: "jürgen@example.edu", err: ErrInvalidEmail},
		{in: "\u212Aarl@example.edu", err: ErrInvalidEmail},
		{in: "\u0430lice@example.edu", err: ErrInvalidEmail},
		{in: "alice@exämple.edu", err: ErrInvalidEmail},
		{in: "alice@example.edu\u00a0", err: ErrInvalidEmail},
		{in: strings.Repeat("a", 64) + "@" + strings.Repeat("b", 185) + ".edu", want: strings.Repeat("a", 64) + "@" + strings.Repeat("b", 185) + ".edu"},
		{in: strings.Repeat("a", 64) + "@" + strings.Repeat("b", 186) + ".edu", err: ErrInvalidEmail},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := CanonicalEmail(tc.in)
			if !errors.Is(err, tc.err) {
				t.Fatalf("CanonicalEmail(%q) err = %v, want %v", tc.in, err, tc.err)
			}
			if got != tc.want {
				t.Errorf("CanonicalEmail(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The struct is unmarshalled straight from a verified ID token, so the claim
// names are part of its contract rather than an implementation detail.
func TestClaimsUnmarshalStandardClaimNames(t *testing.T) {
	c := fromToken(t, `{
		"sub": "8f14e45f",
		"email": "alice@example.edu",
		"email_verified": true,
		"preferred_username": "alice",
		"name": "Alice Example",
		"aud": "ignored"
	}`)

	if c.Subject != "8f14e45f" || c.Email != "alice@example.edu" || !c.EmailVerified || c.PreferredUsername != "alice" || c.Name != "Alice Example" {
		t.Errorf("got %+v", *c)
	}
	if err := json.Unmarshal([]byte(`{"sub": 5}`), new(Claims)); err == nil {
		t.Error("a sub that is no string must not decode")
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

func TestSecretEqual(t *testing.T) {
	for _, c := range []struct {
		got, want string
		ok        bool
	}{
		{"s3cret", "s3cret", true},
		{"s3cret", "s3cre", false},
		{"S3CRET", "s3cret", false},
		{"", "s3cret", false},
		{"", "", false}, // unconfigured is closed, also for a request that brings nothing
		{"anything", "", false},
	} {
		if got := SecretEqual(c.got, c.want); got != c.ok {
			t.Errorf("SecretEqual(%q, %q) = %v, want %v", c.got, c.want, got, c.ok)
		}
	}
}

func TestBearerIs(t *testing.T) {
	for _, c := range []struct {
		header, want string
		ok           bool
	}{
		{"Bearer s3cret", "s3cret", true},
		{"bearer s3cret", "s3cret", true},
		{"Bearer  s3cret ", "s3cret", true},
		{"Bearer other", "s3cret", false},
		{"Basic s3cret", "s3cret", false},
		{"s3cret", "s3cret", false},
		{"", "s3cret", false},
		{"Bearer ", "", false},
		{"Bearer x", "", false},
	} {
		if got := BearerIs(c.header, c.want); got != c.ok {
			t.Errorf("BearerIs(%q, %q) = %v, want %v", c.header, c.want, got, c.ok)
		}
	}
}
