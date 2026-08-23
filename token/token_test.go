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

	issued, err := svc.Issue(ctx, "alice@example.edu", time.Hour, false)
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
		issued, err := svc.Issue(ctx, "alice@example.edu", time.Hour, false)
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
			if _, err := svc.Issue(ctx, tc.subject, tc.ttl, false); !errors.Is(err, tc.want) {
				t.Errorf("Issue: err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNeverExpires(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	svc, _ := newTestService(t, now)

	issued, err := svc.Issue(ctx, "service@example.edu", NeverExpires, false)
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

	issued, err := svc.Issue(ctx, "alice@example.edu", time.Hour, true)
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

	issued, err := svc.Issue(ctx, "alice@example.edu", time.Hour, false)
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

	shortLived, err := svc.Issue(ctx, "alice@example.edu", time.Hour, false)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := svc.Issue(ctx, "alice@example.edu", 24*time.Hour, false); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := svc.Issue(ctx, "bob@example.edu", time.Hour, false); err != nil {
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

	if _, err := svc.Issue(ctx, "alice@example.edu", time.Hour, false); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := svc.Issue(ctx, "bob@example.edu", time.Hour, false); err != nil {
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

	alices, err := svc.Issue(ctx, "alice@example.edu", time.Hour, false)
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
		if _, err := svc.Issue(ctx, subject, time.Hour, false); err != nil {
			t.Fatalf("Issue: %v", err)
		}
	}
	if _, err := svc.Issue(ctx, "service@example.edu", NeverExpires, false); err != nil {
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
