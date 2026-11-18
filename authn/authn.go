// Package authn holds the pieces of request authentication that are the same in
// every service: how a bearer token is taken out of a header, and what an
// authenticated caller is called.
//
// The header helpers were duplicated verbatim. Claims and Identity are here for
// a different reason — not because the code was copied, but because the
// *answer* has to be the same everywhere. A token issued for a person in one
// service and a group resolved for that person in another only line up if both
// mean the same string by "who this is". They did not: openstack-management-api
// fills PreferredUsername from an API token and then resolves groups by email,
// so such a token authenticates cleanly and is a member of nothing.
package authn

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"strings"
)

// Claims is the part of an OIDC ID token the platform acts on. The json tags are the standard claim names, so this can be unmarshalled straight out of a verified token.
//
// Claims come from two places, and Identity treats them differently on purpose. Decoded from JSON (UnmarshalJSON, which is what oidcauth.Verify does with a token), they are what an identity provider asserts, and the e-mail address counts only with email_verified true. Built in code (an API token's subject, the target of X-Act-As, the development user), they are what the service itself has already established, and EmailVerified is not consulted: there is no provider whose word could be wrong.
type Claims struct {
	Subject           string `json:"sub"`
	Email             string `json:"email,omitempty"`
	EmailVerified     bool   `json:"email_verified,omitempty"`
	PreferredUsername string `json:"preferred_username,omitempty"`
	Name              string `json:"name,omitempty"`

	// fromToken is set by UnmarshalJSON: these claims are a provider's assertion, so the address needs email_verified.
	fromToken bool
}

var (
	// ErrNoEmail: the claims carry no e-mail address, so they name nobody. preferred_username and sub are deliberately no substitute: they live in other namespaces, and a username that happens to look like somebody's address would become that person.
	ErrNoEmail = errors.New("authn: no e-mail address in the claims")
	// ErrEmailNotVerified: the token's e-mail address is not marked verified (email_verified is missing or false).
	ErrEmailNotVerified = errors.New("authn: the e-mail address is not verified")
	// ErrInvalidEmail: the value is not a plain ASCII e-mail address (see CanonicalEmail).
	ErrInvalidEmail = errors.New("authn: not a plain ASCII e-mail address")
)

// maxEmailLen is the longest address RFC 5321 lets through a mail system (a 256-octet path minus its angle brackets).
const maxEmailLen = 254

// UnmarshalJSON decodes the standard claims and marks them as coming from a token, so Identity asks for email_verified. email_verified is taken as verified only when it is the JSON boolean true or the string "true" (some providers send a string); anything else, including its absence, is not verified.
func (c *Claims) UnmarshalJSON(data []byte) error {
	var raw struct {
		Subject           string          `json:"sub"`
		Email             string          `json:"email"`
		EmailVerified     json.RawMessage `json:"email_verified"`
		PreferredUsername string          `json:"preferred_username"`
		Name              string          `json:"name"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	verified := false
	switch strings.TrimSpace(string(raw.EmailVerified)) {
	case "true", `"true"`:
		verified = true
	}
	*c = Claims{
		Subject:           raw.Subject,
		Email:             raw.Email,
		EmailVerified:     verified,
		PreferredUsername: raw.PreferredUsername,
		Name:              raw.Name,
		fromToken:         true,
	}
	return nil
}

// Identity returns the canonical identity of the caller — the one string the whole platform keys on: zone ownership, group resolution, token ownership — or "" when the claims name nobody. CheckIdentity says why.
func (c *Claims) Identity() string {
	id, _ := c.CheckIdentity()
	return id
}

// CheckIdentity is Identity with the reason when there is none: ErrNoEmail, ErrEmailNotVerified or ErrInvalidEmail.
//
// The identity is the e-mail address, in its canonical form (CanonicalEmail: trimmed, plain ASCII, lowercased). There is no fallback to preferred_username or sub: those are other namespaces, and a fallback lets an account without an address whose username looks like somebody else's address become that person. For claims decoded from a token the address must also be verified (email_verified true); claims built in code are the service's own word and need no flag (see Claims).
//
// The name deliberately does not say "email": which claim is canonical is a question that is expected to be reopened (Moodle's LTI privacy settings decide whether addresses are released at all). When that day comes, this function is what changes — not a column in three databases.
//
// Service accounts (Keycloak's client-credentials tokens, preferred_username "service-account-…", no address) have no identity here. No service of the platform accepts them: machines authenticate with the services' own API tokens or static bearer tokens. Should one ever have to, the answer is an explicit form that cannot collide with an address (no "@"), added here, not a fallback.
func (c *Claims) CheckIdentity() (string, error) {
	if c == nil {
		return "", ErrNoEmail
	}
	email, err := CanonicalEmail(c.Email)
	if err != nil {
		return "", err
	}
	if c.fromToken && !c.EmailVerified {
		return "", ErrEmailNotVerified
	}
	return email, nil
}

// CanonicalEmail returns the canonical form of an e-mail address as an identity: trimmed of ASCII blanks and lowercased, or an error when it is empty (ErrNoEmail) or not a plain ASCII address (ErrInvalidEmail).
//
// Plain means: printable ASCII without blanks, exactly one "@" with something on both sides, none of the characters only a quoted local part may hold ("(),:;<>[\]), no empty dot-separated part, at most 254 characters. The domain is not required to have a dot ("alertmanager@platform" is a service's identity). Non-ASCII is refused rather than normalised: Unicode case folding maps some non-ASCII letters onto ASCII ones (the Kelvin sign "K" lowercases to "k"), so two different strings would become one identity. The check runs before lowercasing for exactly that reason.
//
// Addresses compare case-insensitively, and identity providers release them in whatever case the directory holds, so "A.B@x" and "a.b@x" arrive as two spellings of one person; every service stores and compares the lowercase form.
func CanonicalEmail(s string) (string, error) {
	// Only ASCII blanks are trimmed: a Unicode space (U+00A0) is part of the value and makes it invalid.
	s = strings.Trim(s, " \t\r\n")
	if s == "" {
		return "", ErrNoEmail
	}
	if len(s) > maxEmailLen {
		return "", ErrInvalidEmail
	}
	for i := 0; i < len(s); i++ {
		if b := s[i]; b <= ' ' || b >= 0x7f {
			return "", ErrInvalidEmail
		}
	}
	if strings.ContainsAny(s, "\"(),:;<>[\\]") {
		return "", ErrInvalidEmail
	}
	local, domain, ok := strings.Cut(s, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") {
		return "", ErrInvalidEmail
	}
	for _, part := range []string{local, domain} {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") || strings.Contains(part, "..") {
			return "", ErrInvalidEmail
		}
	}
	return strings.ToLower(s), nil
}

// CutBearerPrefix strips the "Bearer " scheme and returns the credential.
//
// The scheme is matched case-insensitively because RFC 7235 defines it that
// way: a client sending "bearer <token>" was once rejected as unauthenticated,
// which is a hard failure to diagnose from the outside.
func CutBearerPrefix(header string) (string, bool) {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(header[len(prefix):]), true
}

// Scheme returns the authorization scheme of a header — "Basic", "Token",
// "Bearer" — without the credential that follows it.
//
// It exists so that a rejected request can be logged at all. The header value
// itself must never be: a client sending a valid token under an unexpected
// scheme would write a working credential into the log, and the scheme alone is
// what makes the mistake diagnosable.
func Scheme(header string) string {
	scheme, _, _ := strings.Cut(strings.TrimSpace(header), " ")
	return scheme
}

// SecretEqual reports whether got is the configured secret want, comparing in constant time so the answer's timing tells nothing about how much of a guess was right. An empty want never matches: a static token that was not configured locks the door rather than opening it to every request without one.
func SecretEqual(got, want string) bool {
	if want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// BearerIs reports whether an Authorization header carries the static bearer token want: CutBearerPrefix, then SecretEqual. For the machine-to-machine routes that check one configured token; what to answer when it does not match stays with the caller.
func BearerIs(header, want string) bool {
	got, ok := CutBearerPrefix(header)
	return ok && SecretEqual(got, want)
}
