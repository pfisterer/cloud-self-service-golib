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

import "strings"

// Claims is the part of an OIDC ID token the platform acts on. The json tags
// are the standard claim names, so this can be unmarshalled straight out of a
// verified token.
type Claims struct {
	Subject           string `json:"sub"`
	Email             string `json:"email,omitempty"`
	PreferredUsername string `json:"preferred_username,omitempty"`
	Name              string `json:"name,omitempty"`
}

// Identity returns the canonical identity of the caller — the one string the
// whole platform keys on: zone ownership, group resolution, token ownership.
//
// Today that is the e-mail address, with preferred_username and sub as
// fallbacks for a provider that does not release one. The name deliberately
// does not say "email": which claim is canonical is a question that is expected
// to be reopened. Moodle's LTI privacy settings decide whether names and
// addresses are released at all, and where they are not, an opaque id or a
// matriculation number is all there is. When that day comes, this function is
// what changes — not a column in three databases.
//
// No lowercasing, deliberately. Addresses compare case-insensitively in
// principle, but this value is already stored as the owner of zones and the
// owner of tokens; folding case here would silently stop matching what is on
// disk. That is a migration, not a helper change.
func (c *Claims) Identity() string {
	if c == nil {
		return ""
	}

	for _, candidate := range []string{c.Email, c.PreferredUsername, c.Subject} {
		if v := strings.TrimSpace(candidate); v != "" {
			return v
		}
	}
	return ""
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
