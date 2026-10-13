package authn

import (
	"errors"
	"strings"
)

// ActAsHeader names the person a signed-in admin acts on behalf of, e.g. while working on a ticket. Every service reads the same header and decides on its own whether the caller may: none of them trusts another service's answer.
const ActAsHeader = "X-Act-As"

var (
	// ErrActAsForbidden: the caller may not act on behalf of anybody.
	ErrActAsForbidden = errors.New("acting on behalf of another person is not allowed for this caller")
	// ErrActAsInvalid: the header does not hold an e-mail address.
	ErrActAsInvalid = errors.New("X-Act-As must be an e-mail address")
	// ErrActAsNeedsSignIn: the request carries an API token; acting on behalf of somebody is for people signed in, not for credentials.
	ErrActAsNeedsSignIn = errors.New("acting on behalf of another person needs an interactive sign-in, not an API token")
)

// ActAs returns the identity a request acts as: the actor themself without the header, else the address in it.
//
// The address is trimmed and lowercased, as Identity does. Naming oneself is no switch at all and needs no right. Everything else is refused unless mayActAs holds — an error, never a silent fallback to the actor: an admin who believes they act for somebody else and actually acts as themself would act on the wrong things.
func ActAs(header, actor string, mayActAs bool) (string, error) {
	target := strings.ToLower(strings.TrimSpace(header))
	if target == "" || target == strings.ToLower(strings.TrimSpace(actor)) {
		return actor, nil
	}
	if !strings.Contains(target, "@") || strings.ContainsAny(target, " \t,;") {
		return "", ErrActAsInvalid
	}
	if !mayActAs {
		return "", ErrActAsForbidden
	}
	return target, nil
}

// IsWrite reports whether a request method changes something, which is what is logged with the actor while acting on behalf of somebody.
func IsWrite(method string) bool {
	switch strings.ToUpper(method) {
	case "GET", "HEAD", "OPTIONS":
		return false
	}
	return true
}
