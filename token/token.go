// Package token issues and checks the API tokens a person or a service uses in
// place of an interactive login.
//
// Tokens are deliberately scoped to one service rather than shared across the
// platform: a dyndns token lives in the clear in a home router's configuration,
// and a credential that could also delete OpenStack projects has no business
// being there. So each service keeps its own tokens in its own database, with
// its own prefix — and this package is what they have in common, so that the
// decisions that are easy to get wrong are made once.
//
// Those decisions:
//
//   - The secret is never stored. Only its SHA-256 is, so a database backup, a
//     pgweb session or a pod on the same network holds no working credential.
//     A plain hash is enough because the secret is 128 bits from crypto/rand:
//     there is nothing to brute-force and no need for a slow KDF.
//   - An expired token is absent, at lookup time. Cleanup happens when someone
//     lists their tokens, which is far too rare to rely on for security.
//   - A token that does not expire has to be asked for by name. A zero TTL is
//     rejected rather than treated as "forever", so a forgotten configuration
//     value cannot quietly mint a permanent credential.
//
// A Subject is whatever the platform calls an identity — see authn.Identity. It
// is deliberately not called a user: the reconciler that provisions course VMs
// will hold tokens too, under a service identity, and nothing here should have
// to care about the difference.
package token

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// secretBytes is the entropy of a token, 128 bits. The number is what makes
// storing a plain SHA-256 rather than a slow KDF defensible.
const secretBytes = 16

// displayChars is how much of the random part is kept in the clear, on top of
// the service prefix, so a person can tell two of their tokens apart in a list.
// Eight hex characters leave 96 bits secret.
const displayChars = 8

// NeverExpires is the TTL of a token that stays valid until it is revoked.
//
// It is -1 and not 0 so that it cannot be arrived at by accident: a TTL
// computed from a missing configuration value is 0, and Issue rejects that.
// Handing out a permanent credential should take saying so.
const NeverExpires = time.Duration(-1)

// MaxDescriptionLen caps the note a person may attach to a token. Long enough
// for "ddclient on the router at home", short enough that a table column can
// show all of it — and a bound is needed at all because this is arbitrary text
// from a request body.
const MaxDescriptionLen = 100

// lastUsedResolution is how precisely LastUsedAt is kept. Every authenticated
// request would otherwise be a write, for an answer nobody reads at that
// resolution: what the field is for is "is this token still in use, or can I
// revoke it", and a minute is far finer than that question needs.
const lastUsedResolution = time.Minute

var (
	// ErrNotFound is returned for a token that does not exist, and for one that
	// has expired — the caller must not be able to tell those apart.
	ErrNotFound = errors.New("token not found")
	// ErrInvalidTTL is returned for a TTL of zero or a negative one that is not
	// NeverExpires.
	ErrInvalidTTL = errors.New("token: TTL must be positive or NeverExpires")
	// ErrNoSubject is returned when no identity was given to issue a token for.
	ErrNoSubject = errors.New("token: subject is required")
	// ErrDescriptionTooLong is returned for a description over MaxDescriptionLen.
	ErrDescriptionTooLong = fmt.Errorf("token: description must be at most %d characters", MaxDescriptionLen)
	// ErrTTLTooLong is returned by TTLPolicy.Resolve for a lifetime over its
	// maximum.
	ErrTTLTooLong = errors.New("token: requested lifetime is longer than allowed")
	// ErrNeverExpiresNotAllowed is returned by TTLPolicy.Resolve when a caller
	// asks for a token without an expiry and the policy forbids it.
	ErrNeverExpiresNotAllowed = errors.New("token: tokens without an expiry are not allowed here")
)

// RequestedNever is what an API client sends as the requested lifetime to ask
// for a token that does not expire. It is -1, the same as NeverExpires, so that
// there is one way of saying "no expiry" from the request body down to the
// database row rather than two conventions to keep in step.
const RequestedNever = -1

// TTLPolicy answers what lifetime a caller may have, given what they asked for.
//
// It lives here rather than in each service because the decision table is the
// same in both and the mistakes are the interesting part: silently shortening a
// lifetime, or letting a missing configuration value mean "forever". The VALUES
// are per deployment — one service's tokens sit in home routers, another's can
// delete OpenStack projects.
type TTLPolicy struct {
	// Default applies when the caller asks for nothing. Must be positive: a
	// zero here is a missing configuration value, and Resolve says so rather
	// than picking something.
	Default time.Duration
	// Max is the longest lifetime a caller may ask for. Zero or less means no
	// bound, which is what an operator who set nothing gets — a request for
	// "never" is still refused unless AllowNever says otherwise.
	Max time.Duration
	// AllowNever permits a token with no expiry at all.
	AllowNever bool
}

// Resolve turns a requested lifetime in hours into a duration.
//
// Zero means "no preference" and yields Default. RequestedNever asks for no
// expiry. Anything else is taken literally and checked against Max.
//
// A request over Max is an error and not a quiet clamp: a token that stops
// working earlier than the person was told is an outage nobody traces back to
// the moment they created it.
func (p TTLPolicy) Resolve(requestedHours int) (time.Duration, error) {
	switch {
	case requestedHours == 0:
		if p.Default <= 0 {
			return 0, fmt.Errorf("%w: no default lifetime is configured", ErrInvalidTTL)
		}
		return p.Default, nil

	case requestedHours == RequestedNever:
		if !p.AllowNever {
			return 0, ErrNeverExpiresNotAllowed
		}
		return NeverExpires, nil

	case requestedHours < 0:
		return 0, fmt.Errorf("%w: %d hours", ErrInvalidTTL, requestedHours)

	default:
		ttl := time.Duration(requestedHours) * time.Hour
		if p.Max > 0 && ttl > p.Max {
			return 0, fmt.Errorf("%w: %s, maximum is %s", ErrTTLTooLong, ttl, p.Max)
		}
		return ttl, nil
	}
}

// Record is one issued token, minus the secret — which is what is stored.
type Record struct {
	// ID identifies the token to its owner, for listing and revoking. Assigned
	// by the Store.
	ID uint
	// Subject is the identity the token acts as: a person's canonical identity
	// (authn.Claims.Identity) or a service identity. Every request the token
	// authenticates is treated as coming from it.
	Subject string
	// Hash is the SHA-256 of the secret, hex encoded. The only form persisted.
	Hash string
	// Prefix is the leading, non-secret part of the token.
	Prefix string
	// ReadOnly limits the token to reads. What that means is the calling
	// service's decision — for both services today it is "GET only".
	ReadOnly bool
	// Description is what its owner wrote down about it: which script, which
	// machine, why. A memory aid and nothing else — never a permission, never a
	// key to anything. Being arbitrary user text, it is never logged.
	Description string
	// CreatedAt is when the token was issued.
	CreatedAt time.Time
	// ExpiresAt is when it stops being accepted. The zero time means never,
	// which is what NeverExpires produces.
	ExpiresAt time.Time
	// LastUsedAt is when the token last authenticated a request, to the nearest
	// lastUsedResolution. The zero time means it never has.
	//
	// This is what makes revoking safe to do: a description says what someone
	// intended a token for, this says whether anything still relies on it.
	LastUsedAt time.Time
}

// Expired reports whether the token is past its expiry at the given time. A
// record with no expiry never is.
func (r Record) Expired(now time.Time) bool {
	return !r.ExpiresAt.IsZero() && !now.Before(r.ExpiresAt)
}

// Issued is a newly created token: the record, plus the one and only moment the
// secret exists outside the caller's memory. It is not persisted and cannot be
// recovered afterwards.
type Issued struct {
	Record
	Secret string
}

// Store is the persistence a Service needs. Implementations exist for GORM
// (package tokengorm) and in memory (NewMemoryStore).
//
// Delete and List take the subject as well as the ID on purpose: a token is
// revoked by its owner, and an implementation that matched on the ID alone
// would let anyone guess a number and revoke somebody else's credential.
type Store interface {
	// Insert stores the record and returns it with its assigned ID.
	Insert(ctx context.Context, rec Record) (Record, error)
	// ByHash returns the record with this hash, or ErrNotFound. Expiry is not
	// its concern — the Service applies that.
	ByHash(ctx context.Context, hash string) (Record, error)
	// BySubject returns every record belonging to the subject, expired ones
	// included.
	BySubject(ctx context.Context, subject string) ([]Record, error)
	// Delete removes the subject's token with this ID, or returns ErrNotFound.
	Delete(ctx context.Context, subject string, id uint) error
	// MarkUsed records that the token was used at the given time. No subject:
	// the Service calls it for a token it has just resolved by hash, so there is
	// no owner to check against, and this writes nothing a caller could learn
	// something from.
	MarkUsed(ctx context.Context, id uint, at time.Time) error
	// DeleteExpired removes every record that expired before the given time and
	// returns how many went.
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// Service issues and checks tokens of one service's flavour.
type Service struct {
	prefix string
	store  Store
	now    func() time.Time
}

// NewService returns a Service issuing tokens under the given prefix — the
// string that tells this service's tokens apart from an OIDC bearer token and
// from another service's, e.g. "dynz_token_" or "os_mgt_".
func NewService(prefix string, store Store) *Service {
	return &Service{prefix: prefix, store: store, now: time.Now}
}

// WithClock returns a copy of the Service that reads the time from now. For
// tests — a copy rather than a mutation, so that a test holding both cannot
// silently change the original's behaviour under itself.
func (s *Service) WithClock(now func() time.Time) *Service {
	copied := *s
	copied.now = now
	return &copied
}

// Prefix returns the prefix this Service issues under.
func (s *Service) Prefix() string { return s.prefix }

// Owns reports whether a credential looks like one of this Service's tokens.
// This is how an authentication middleware decides between looking a token up
// here and verifying it as an OIDC bearer token.
//
// It says nothing about validity: an attacker can put the prefix in front of
// anything. It only routes.
func (s *Service) Owns(secret string) bool {
	return strings.HasPrefix(secret, s.prefix)
}

// IssueOptions is everything about a token that its owner gets to choose.
//
// A struct rather than parameters: Issue had a TTL and a bool, and the third
// thing to decide would have made it four positional arguments with two of them
// unreadable at the call site. Whatever comes next — a scope, a rate budget —
// belongs in here too.
type IssueOptions struct {
	// TTL must be positive, or NeverExpires for a token that stays valid until
	// revoked. There is no default: a zero value is rejected rather than
	// silently meaning one thing or the other.
	//
	// Whether NeverExpires may be asked for at all is the calling service's
	// policy, not this package's — both make it a configuration option, off by
	// default, because a permanent credential is a decision an operator takes.
	TTL time.Duration
	// ReadOnly limits the token to reads.
	ReadOnly bool
	// Description is the owner's note about the token. Trimmed, and rejected
	// over MaxDescriptionLen. Optional.
	Description string
}

// Issue creates a token for the subject and returns it, including the secret,
// which the caller has exactly this one chance to pass on.
func (s *Service) Issue(ctx context.Context, subject string, opts IssueOptions) (*Issued, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return nil, ErrNoSubject
	}
	if opts.TTL <= 0 && opts.TTL != NeverExpires {
		return nil, fmt.Errorf("%w, got %s", ErrInvalidTTL, opts.TTL)
	}

	description := strings.TrimSpace(opts.Description)
	// Counted in runes, not bytes: the limit is about what fits on a screen, and
	// a note in German would otherwise be shorter than one in English.
	if len([]rune(description)) > MaxDescriptionLen {
		return nil, ErrDescriptionTooLong
	}

	secret, err := generateSecret(s.prefix)
	if err != nil {
		return nil, fmt.Errorf("token.Issue: %w", err)
	}

	now := s.now()
	rec := Record{
		Subject:     subject,
		Hash:        Hash(secret),
		Prefix:      displayPrefix(secret),
		ReadOnly:    opts.ReadOnly,
		Description: description,
		CreatedAt:   now,
	}
	if opts.TTL != NeverExpires {
		rec.ExpiresAt = now.Add(opts.TTL)
	}

	stored, err := s.store.Insert(ctx, rec)
	if err != nil {
		// The secret is not in the error: this is logged.
		return nil, fmt.Errorf("token.Issue: storing token for %q: %w", subject, err)
	}

	return &Issued{Record: stored, Secret: secret}, nil
}

// Lookup resolves a secret to its record, or returns ErrNotFound.
//
// An expired token is ErrNotFound, not a distinct answer: the caller has no use
// for the difference and an attacker would.
//
// It also records the use, at most once per lastUsedResolution. A failure to
// write that is deliberately ignored: this sits on the authentication path of
// every request, and a valid credential must not be refused because a
// bookkeeping column could not be updated. The cost is a LastUsedAt that can
// lag; the alternative is an outage for a field nobody authenticates against.
func (s *Service) Lookup(ctx context.Context, secret string) (*Record, error) {
	rec, err := s.store.ByHash(ctx, Hash(secret))
	if err != nil {
		return nil, err
	}
	now := s.now()
	if rec.Expired(now) {
		return nil, ErrNotFound
	}

	if now.Sub(rec.LastUsedAt) >= lastUsedResolution {
		if err := s.store.MarkUsed(ctx, rec.ID, now); err == nil {
			// Only on success, so the returned record says what is stored.
			rec.LastUsedAt = now
		}
	}
	return &rec, nil
}

// List returns the subject's valid tokens and deletes any expired ones it finds
// on the way.
//
// The cleanup is here rather than in Lookup because it is the only place where
// deleting is free — the rows are in hand. It is a tidying, not a security
// measure: Lookup rejects an expired token whether or not it was ever listed.
func (s *Service) List(ctx context.Context, subject string) ([]Record, error) {
	all, err := s.store.BySubject(ctx, subject)
	if err != nil {
		return nil, err
	}

	now := s.now()
	valid := make([]Record, 0, len(all))
	for _, rec := range all {
		if rec.Expired(now) {
			// A failure here is not the caller's problem: they asked for their
			// tokens, and the expired ones are excluded either way.
			_ = s.store.Delete(ctx, subject, rec.ID)
			continue
		}
		valid = append(valid, rec)
	}
	return valid, nil
}

// Revoke deletes one of the subject's tokens, or returns ErrNotFound.
func (s *Service) Revoke(ctx context.Context, subject string, id uint) error {
	return s.store.Delete(ctx, subject, id)
}

// DeleteExpired removes every expired token of every subject. For a periodic
// job; List already clears the ones it walks past.
func (s *Service) DeleteExpired(ctx context.Context) (int64, error) {
	return s.store.DeleteExpired(ctx, s.now())
}

// Hash is the one-way mapping from a secret to what is stored.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// generateSecret returns prefix + 128 bits of hex.
func generateSecret(prefix string) (string, error) {
	b := make([]byte, secretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	return prefix + hex.EncodeToString(b), nil
}

// displayPrefix returns the identifying head of a token: the service prefix and
// the first few characters of the random part.
func displayPrefix(secret string) string {
	if len(secret) <= displayChars {
		return secret
	}
	// Deliberately counted from the end, so that the amount kept secret does not
	// depend on how long the service's prefix happens to be.
	return secret[:len(secret)-(secretBytes*2-displayChars)]
}
