// Package credential decides who a request is from, by its Authorization header: a bearer that one of the service's API tokens owns is looked up in the token store, any other bearer is verified as an OIDC ID token. Every service of the platform repeated these steps in its auth middleware, and the order and the failure cases have to be the same everywhere: a token with the service's prefix is never sent to the identity provider, and a token nobody can judge because the provider is away is not a rejected token.
//
// What a refusal looks like (status, text, the service's caller type) stays with each service; this package only says which case it is.
package credential

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/pfisterer/cloud-self-service-golib/authn"
	"github.com/pfisterer/cloud-self-service-golib/oidcauth"
	"github.com/pfisterer/cloud-self-service-golib/token"
)

var (
	// ErrMissing: no bearer credential at all — no header, another scheme, or an empty bearer. Services answer 401.
	ErrMissing = errors.New("credential: no bearer credential")
	// ErrRejected: a credential that is not accepted — an API token that is unknown, expired or revoked (the store does not tell which), or an ID token that fails verification. The error wraps the cause for the log. Services answer 401.
	ErrRejected = errors.New("credential: credential rejected")
	// ErrKeysUnavailable: an ID token that cannot be judged right now, because the provider's keys cannot be fetched (oidcauth.ErrKeysUnavailable). Services answer 503, not 401: a 401 sends the browser into a login that cannot work while the provider is away.
	ErrKeysUnavailable = oidcauth.ErrKeysUnavailable
	// ErrNoVerifier: a bearer that is not an API token, and no OIDC verifier configured to judge it. A configuration of the service, not a fault of the caller; what it answers is the service's decision.
	ErrNoVerifier = errors.New("credential: no OIDC verifier configured")
)

// Tokens is the part of a token.Service the resolver uses; *token.Service implements it. Owns only routes (a prefix), Lookup decides.
type Tokens interface {
	Owns(secret string) bool
	Lookup(ctx context.Context, secret string) (*token.Record, error)
}

// Verifier is the part of an oidcauth.Verifier the resolver uses; *oidcauth.Verifier implements it, and a test can stand in for it. A failure it cannot judge because the provider's keys cannot be fetched must wrap oidcauth.ErrKeysUnavailable.
type Verifier interface {
	Verify(ctx context.Context, rawIDToken string) (*authn.Claims, error)
}

// Principal is who a request is from, as far as authentication can tell. Who that is in the service's terms (its caller type, its roles, acting on behalf) is built from it per service.
type Principal struct {
	// Claims are the verified ID token's claims; for an API token, only Email, holding the token's subject — tokens are issued under the identity (authn.Claims.Identity) of their owner, so Claims.Identity() is the same string for both.
	Claims *authn.Claims
	// Token is the API token's record; nil for an ID token.
	Token *token.Record
	// ReadOnly: an API token issued read-only. An ID token is never read-only.
	ReadOnly bool
}

// ViaAPIToken reports whether the principal authenticated with an API token rather than by signing in.
func (p Principal) ViaAPIToken() bool { return p.Token != nil }

// Identity is the canonical identity of the principal (authn.Claims.Identity).
func (p Principal) Identity() string { return p.Claims.Identity() }

// Resolve authenticates the value of an Authorization header. tokens may be nil for a service without API tokens, and verifier nil for one without sign-in.
//
// The cases, in this order: no bearer is ErrMissing; a bearer tokens owns is looked up — not found is ErrRejected, any other failure of the store comes back wrapped and is the service's own (500); every other bearer goes to verifier — none is ErrNoVerifier, keys that cannot be fetched are ErrKeysUnavailable, any other failure is ErrRejected. A token with the service's prefix is never sent to the verifier, whatever the store says.
func Resolve(ctx context.Context, header string, tokens Tokens, verifier Verifier) (Principal, error) {
	raw, ok := authn.CutBearerPrefix(header)
	if !ok || raw == "" {
		return Principal{}, ErrMissing
	}
	if tokens != nil && !isNilPointer(tokens) && tokens.Owns(raw) {
		rec, err := tokens.Lookup(ctx, raw)
		switch {
		case errors.Is(err, token.ErrNotFound):
			return Principal{}, rejected{err}
		case err != nil:
			return Principal{}, fmt.Errorf("credential: token lookup: %w", err)
		case rec == nil:
			return Principal{}, rejected{token.ErrNotFound}
		}
		return Principal{Claims: &authn.Claims{Email: rec.Subject}, Token: rec, ReadOnly: rec.ReadOnly}, nil
	}
	if verifier == nil || isNilPointer(verifier) {
		return Principal{}, ErrNoVerifier
	}
	claims, err := verifier.Verify(ctx, raw)
	if err != nil {
		if errors.Is(err, oidcauth.ErrKeysUnavailable) {
			return Principal{}, err
		}
		return Principal{}, rejected{err}
	}
	if claims == nil {
		return Principal{}, rejected{errors.New("no claims")}
	}
	return Principal{Claims: claims}, nil
}

// rejected is ErrRejected with its cause.
type rejected struct{ cause error }

func (r rejected) Error() string   { return ErrRejected.Error() + ": " + r.cause.Error() }
func (r rejected) Unwrap() []error { return []error{ErrRejected, r.cause} }

// Cause is what a rejection was made from (token.ErrNotFound for an API token, the verifier's error for an ID token), for a log line or a service that has always shown it; err itself for any other error.
func Cause(err error) error {
	if r, ok := errors.AsType[rejected](err); ok {
		return r.cause
	}
	return err
}

// isNilPointer catches a nil *token.Service or *oidcauth.Verifier passed as the interface, which is not a nil interface.
func isNilPointer(v any) bool {
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Pointer && rv.IsNil()
}
