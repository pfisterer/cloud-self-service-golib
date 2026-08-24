package token

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const testPrefix = "test_token_"

func newTestService(t *testing.T, now time.Time) (*Service, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	svc := NewService(testPrefix, store).WithClock(func() time.Time { return now })
	return svc, store
}

func TestIssueSecretShape(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, time.Now())

	issued, err := svc.Issue(ctx, "alice@example.edu", IssueOptions{TTL: time.Hour})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if !strings.HasPrefix(issued.Secret, testPrefix) {
		t.Errorf("secret %q does not carry the prefix", issued.Secret)
	}
	// prefix + 16 random bytes as hex.
	if want := len(testPrefix) + secretBytes*2; len(issued.Secret) != want {
		t.Errorf("secret length = %d, want %d", len(issued.Secret), want)
	}
	if issued.Hash != Hash(issued.Secret) {
		t.Error("stored hash does not match the secret")
	}
	if strings.Contains(issued.Hash, issued.Secret) {
		t.Error("the hash contains the secret")
	}
	// The display prefix must identify without revealing: it keeps the service
	// prefix plus displayChars of the random part, leaving the rest secret.
	if want := len(testPrefix) + displayChars; len(issued.Prefix) != want {
		t.Errorf("display prefix length = %d, want %d (%q)", len(issued.Prefix), want, issued.Prefix)
	}
	if !strings.HasPrefix(issued.Secret, issued.Prefix) {
		t.Errorf("display prefix %q is not the head of the secret", issued.Prefix)
	}
}

func TestIssueSecretsAreUnique(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, time.Now())

	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		issued, err := svc.Issue(ctx, "alice@example.edu", IssueOptions{TTL: time.Hour})
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if _, dup := seen[issued.Secret]; dup {
			t.Fatal("generated the same secret twice")
		}
		seen[issued.Secret] = struct{}{}
	}
}

func TestIssueRejectsBadInput(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, time.Now())

	cases := []struct {
		name    string
		subject string
		ttl     time.Duration
		want    error
	}{
		{name: "no subject", subject: "", ttl: time.Hour, want: ErrNoSubject},
		{name: "blank subject", subject: "   ", ttl: time.Hour, want: ErrNoSubject},
		// The case this guard exists for: a TTL computed from a missing
		// configuration value is zero, and must not mint a permanent credential.
		{name: "zero TTL", subject: "alice", ttl: 0, want: ErrInvalidTTL},
		{name: "negative TTL that is not the sentinel", subject: "alice", ttl: -time.Hour, want: ErrInvalidTTL},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Issue(ctx, tc.subject, IssueOptions{TTL: tc.ttl}); !errors.Is(err, tc.want) {
				t.Errorf("Issue: err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNeverExpires(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	svc, _ := newTestService(t, now)

	issued, err := svc.Issue(ctx, "service@example.edu", IssueOptions{TTL: NeverExpires})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !issued.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %v, want the zero time", issued.ExpiresAt)
	}

	// A provisioning token that silently expired at three in the morning is the
	// failure this is meant to rule out.
	far := svc.WithClock(func() time.Time { return now.AddDate(10, 0, 0) })
	if _, err := far.Lookup(ctx, issued.Secret); err != nil {
		t.Errorf("Lookup ten years on: %v", err)
	}
}

func TestLookup(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	svc, _ := newTestService(t, now)

	issued, err := svc.Issue(ctx, "alice@example.edu", IssueOptions{TTL: time.Hour, ReadOnly: true})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	t.Run("resolves to its record", func(t *testing.T) {
		rec, err := svc.Lookup(ctx, issued.Secret)
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if rec.Subject != "alice@example.edu" || !rec.ReadOnly {
			t.Errorf("got %+v", rec)
		}
	})

	t.Run("unknown secret", func(t *testing.T) {
		if _, err := svc.Lookup(ctx, testPrefix+"0000"); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	// The property that matters most: the lazy cleanup in List only runs when
	// the owner happens to look, so expiry has to be enforced on every lookup.
	t.Run("expired is indistinguishable from absent", func(t *testing.T) {
		expired := svc.WithClock(func() time.Time { return now.Add(2 * time.Hour) })
		if _, err := expired.Lookup(ctx, issued.Secret); !errors.Is(err, ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestLookupExactlyAtExpiry(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	svc, _ := newTestService(t, now)

	issued, err := svc.Issue(ctx, "alice@example.edu", IssueOptions{TTL: time.Hour})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// The boundary is closed: at the expiry instant the token is already gone,
	// which is the safe direction to round.
	at := svc.WithClock(func() time.Time { return issued.ExpiresAt })
	if _, err := at.Lookup(ctx, issued.Secret); !errors.Is(err, ErrNotFound) {
		t.Errorf("at expiry: err = %v, want ErrNotFound", err)
	}

	just := svc.WithClock(func() time.Time { return issued.ExpiresAt.Add(-time.Nanosecond) })
	if _, err := just.Lookup(ctx, issued.Secret); err != nil {
		t.Errorf("a nanosecond before expiry: %v", err)
	}
}

func TestListDropsAndDeletesExpired(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	svc, store := newTestService(t, now)

	shortLived, err := svc.Issue(ctx, "alice@example.edu", IssueOptions{TTL: time.Hour})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := svc.Issue(ctx, "alice@example.edu", IssueOptions{TTL: 24 * time.Hour}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := svc.Issue(ctx, "bob@example.edu", IssueOptions{TTL: time.Hour}); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	later := svc.WithClock(func() time.Time { return now.Add(2 * time.Hour) })
	list, err := later.List(ctx, "alice@example.edu")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(list) != 1 {
		t.Fatalf("got %d tokens, want 1", len(list))
	}
	if list[0].ID == shortLived.ID {
		t.Error("the expired token was returned")
	}
	if _, err := store.ByHash(ctx, shortLived.Hash); !errors.Is(err, ErrNotFound) {
		t.Error("the expired token was returned but not deleted")
	}
	// Bob's token expired too, but it is not Alice's to clean up on her request.
	if n, _ := store.DeleteExpired(ctx, now.Add(2*time.Hour)); n != 1 {
		t.Errorf("expected bob's expired token to remain, deleted %d", n)
	}
}

func TestListIsScopedToTheSubject(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, time.Now())

	if _, err := svc.Issue(ctx, "alice@example.edu", IssueOptions{TTL: time.Hour}); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := svc.Issue(ctx, "bob@example.edu", IssueOptions{TTL: time.Hour}); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	list, err := svc.List(ctx, "alice@example.edu")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Subject != "alice@example.edu" {
		t.Errorf("got %+v", list)
	}
}

// Revoking by ID alone would let anyone guess a number and take away somebody
// else's credential.
func TestRevokeRequiresOwnership(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, time.Now())

	alices, err := svc.Issue(ctx, "alice@example.edu", IssueOptions{TTL: time.Hour})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if err := svc.Revoke(ctx, "mallory@example.edu", alices.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking somebody else's token: err = %v, want ErrNotFound", err)
	}
	if _, err := svc.Lookup(ctx, alices.Secret); err != nil {
		t.Fatal("the token was revoked by a stranger")
	}

	if err := svc.Revoke(ctx, "alice@example.edu", alices.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := svc.Lookup(ctx, alices.Secret); !errors.Is(err, ErrNotFound) {
		t.Error("the token survived its own revocation")
	}
}

func TestOwns(t *testing.T) {
	svc, _ := newTestService(t, time.Now())

	if !svc.Owns(testPrefix + "abc") {
		t.Error("did not recognise its own prefix")
	}
	if svc.Owns("dynz_token_abc") {
		t.Error("claimed another service's token")
	}
	// Owns routes, it does not authenticate: anyone can put the prefix in front
	// of anything, and Lookup is what decides.
	if !svc.Owns(testPrefix) {
		t.Error("routing must not depend on what follows the prefix")
	}
}

func TestRecordExpired(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	if (Record{}).Expired(now) {
		t.Error("a record without an expiry must never expire")
	}
	if !(Record{ExpiresAt: now.Add(-time.Second)}).Expired(now) {
		t.Error("a past expiry must count as expired")
	}
	if (Record{ExpiresAt: now.Add(time.Second)}).Expired(now) {
		t.Error("a future expiry must not")
	}
}

func TestDeleteExpiredSweepsEverySubject(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	svc, _ := newTestService(t, now)

	for _, subject := range []string{"alice@example.edu", "bob@example.edu"} {
		if _, err := svc.Issue(ctx, subject, IssueOptions{TTL: time.Hour}); err != nil {
			t.Fatalf("Issue: %v", err)
		}
	}
	if _, err := svc.Issue(ctx, "service@example.edu", IssueOptions{TTL: NeverExpires}); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	later := svc.WithClock(func() time.Time { return now.Add(2 * time.Hour) })
	n, err := later.DeleteExpired(ctx)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if n != 2 {
		t.Errorf("deleted %d, want 2 — the never-expiring token must survive", n)
	}
}

func TestIssueTrimsTheDescription(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, time.Now())

	issued, err := svc.Issue(ctx, "alice@example.edu", IssueOptions{
		TTL:         time.Hour,
		Description: "  ddclient on the router at home \n",
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if issued.Description != "ddclient on the router at home" {
		t.Errorf("Description = %q, want it trimmed", issued.Description)
	}
}

// The limit is about what fits in a column, so it counts characters and not
// bytes: a note in German must not be shorter than the same note in English.
func TestIssueRejectsAnOverlongDescription(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestService(t, time.Now())

	cases := []struct {
		name        string
		description string
		want        error
	}{
		{"at the limit", strings.Repeat("a", MaxDescriptionLen), nil},
		{"one over", strings.Repeat("a", MaxDescriptionLen+1), ErrDescriptionTooLong},
		// Three bytes each, so this is well over the byte limit and exactly at
		// the character limit. It must be accepted.
		{"multibyte at the limit", strings.Repeat("ü", MaxDescriptionLen), nil},
		{"multibyte one over", strings.Repeat("ü", MaxDescriptionLen+1), ErrDescriptionTooLong},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Issue(ctx, "alice@example.edu", IssueOptions{
				TTL: time.Hour, Description: tc.description,
			})
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestLookupRecordsTheUse(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	svc, store := newTestService(t, now)

	issued, err := svc.Issue(ctx, "alice@example.edu", IssueOptions{TTL: time.Hour})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !issued.LastUsedAt.IsZero() {
		t.Error("a token that was just issued has not been used")
	}

	rec, err := svc.Lookup(ctx, issued.Secret)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !rec.LastUsedAt.Equal(now) {
		t.Errorf("returned LastUsedAt = %v, want %v", rec.LastUsedAt, now)
	}
	stored, err := store.ByHash(ctx, issued.Hash)
	if err != nil {
		t.Fatalf("ByHash: %v", err)
	}
	if !stored.LastUsedAt.Equal(now) {
		t.Errorf("stored LastUsedAt = %v, want %v", stored.LastUsedAt, now)
	}
}

// Every authenticated request would otherwise be a database write for a field
// nobody reads at that resolution.
func TestLookupThrottlesTheUseWrite(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	svc, store := newTestService(t, now)

	issued, err := svc.Issue(ctx, "alice@example.edu", IssueOptions{TTL: time.Hour})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := svc.Lookup(ctx, issued.Secret); err != nil {
		t.Fatalf("first Lookup: %v", err)
	}

	within := svc.WithClock(func() time.Time { return now.Add(lastUsedResolution / 2) })
	if _, err := within.Lookup(ctx, issued.Secret); err != nil {
		t.Fatalf("second Lookup: %v", err)
	}
	stored, _ := store.ByHash(ctx, issued.Hash)
	if !stored.LastUsedAt.Equal(now) {
		t.Errorf("LastUsedAt = %v, want it left at %v inside the window", stored.LastUsedAt, now)
	}

	after := now.Add(lastUsedResolution + time.Second)
	beyond := svc.WithClock(func() time.Time { return after })
	if _, err := beyond.Lookup(ctx, issued.Secret); err != nil {
		t.Fatalf("third Lookup: %v", err)
	}
	stored, _ = store.ByHash(ctx, issued.Hash)
	if !stored.LastUsedAt.Equal(after) {
		t.Errorf("LastUsedAt = %v, want %v once the window has passed", stored.LastUsedAt, after)
	}
}

// failingMarkUsed is a Store whose bookkeeping write always fails.
type failingMarkUsed struct{ Store }

func (failingMarkUsed) MarkUsed(context.Context, uint, time.Time) error {
	return errors.New("column is on fire")
}

// A valid credential must not be refused because a statistics column could not
// be written — this sits on the authentication path of every request.
func TestLookupSurvivesAFailingUseWrite(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	svc := NewService(testPrefix, failingMarkUsed{store}).WithClock(func() time.Time { return now })

	issued, err := svc.Issue(ctx, "alice@example.edu", IssueOptions{TTL: time.Hour})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	rec, err := svc.Lookup(ctx, issued.Secret)
	if err != nil {
		t.Fatalf("Lookup failed because MarkUsed did: %v", err)
	}
	// And it must not claim a use it could not record.
	if !rec.LastUsedAt.IsZero() {
		t.Errorf("LastUsedAt = %v, want the zero time when the write failed", rec.LastUsedAt)
	}
}

func TestTTLPolicyResolve(t *testing.T) {
	policy := TTLPolicy{Default: 24 * time.Hour, Max: 365 * 24 * time.Hour}
	permissive := TTLPolicy{Default: 24 * time.Hour, Max: 365 * 24 * time.Hour, AllowNever: true}
	unbounded := TTLPolicy{Default: time.Hour}

	cases := []struct {
		name    string
		policy  TTLPolicy
		hours   int
		want    time.Duration
		wantErr error
	}{
		{"nothing asked for", policy, 0, 24 * time.Hour, nil},
		{"within the maximum", policy, 720, 720 * time.Hour, nil},
		{"exactly the maximum", policy, 365 * 24, 365 * 24 * time.Hour, nil},
		// Refused rather than clamped: see Resolve.
		{"over the maximum", policy, 365*24 + 1, 0, ErrTTLTooLong},
		{"never, not allowed", policy, RequestedNever, 0, ErrNeverExpiresNotAllowed},
		{"never, allowed", permissive, RequestedNever, NeverExpires, nil},
		{"negative but not never", policy, -2, 0, ErrInvalidTTL},
		// No maximum configured is no bound — but "never" still needs saying.
		{"no maximum", unbounded, 100000, 100000 * time.Hour, nil},
		{"no maximum, never still refused", unbounded, RequestedNever, 0, ErrNeverExpiresNotAllowed},
		// A missing default is a configuration error, not a licence to pick.
		{"no default", TTLPolicy{}, 0, 0, ErrInvalidTTL},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.policy.Resolve(tc.hours)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("ttl = %s, want %s", got, tc.want)
			}
		})
	}
}

// The whole point of RequestedNever being -1: what an API client sends and what
// the Service takes are the same value, so nothing has to translate.
func TestRequestedNeverIsNeverExpires(t *testing.T) {
	if time.Duration(RequestedNever) != NeverExpires {
		t.Errorf("RequestedNever = %d, NeverExpires = %d — they must not drift", RequestedNever, NeverExpires)
	}
}
