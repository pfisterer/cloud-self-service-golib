package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

var fast = Policy{Total: 2 * time.Second, Initial: time.Millisecond, Max: 4 * time.Millisecond}

func TestSucceedsAfterFailures(t *testing.T) {
	calls, retries := 0, 0
	p := fast
	p.OnRetry = func(attempt int, err error, wait time.Duration) {
		retries++
		if attempt != retries || err == nil || wait <= 0 || wait > p.Max {
			t.Fatalf("OnRetry(%d, %v, %v) after %d retries", attempt, err, wait, retries)
		}
	}
	err := Do(context.Background(), p, func(context.Context) error {
		calls++
		if calls < 4 {
			return errors.New("connection refused")
		}
		return nil
	})
	if err != nil || calls != 4 || retries != 3 {
		t.Fatalf("err=%v calls=%d retries=%d, want nil 4 3", err, calls, retries)
	}
}

func TestGivesUpAfterTotal(t *testing.T) {
	want := errors.New("still down")
	start := time.Now()
	err := Do(context.Background(), Policy{Total: 50 * time.Millisecond, Initial: 5 * time.Millisecond, Max: 10 * time.Millisecond}, func(context.Context) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("err=%v, want %v", err, want)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("took %v, Total was 50ms", d)
	}
}

func TestPermanentStopsAtOnce(t *testing.T) {
	want := errors.New("password authentication failed")
	calls := 0
	err := Do(context.Background(), fast, func(context.Context) error {
		calls++
		return Permanent(want)
	})
	if err != want || calls != 1 {
		t.Fatalf("err=%v calls=%d, want the unwrapped error after 1 call", err, calls)
	}
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil) must stay nil")
	}
}

func TestContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	want := errors.New("down")
	calls := 0
	err := Do(ctx, Policy{Total: time.Minute, Initial: 10 * time.Millisecond}, func(context.Context) error {
		calls++
		cancel()
		return want
	})
	if !errors.Is(err, want) || calls != 1 {
		t.Fatalf("err=%v calls=%d, want the last error after 1 call", err, calls)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if err := Do(ctx2, fast, func(context.Context) error { t.Fatal("fn ran on an ended context"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
}

func TestZeroPolicyDefaults(t *testing.T) {
	calls := 0
	if err := Do(context.Background(), Policy{}, func(context.Context) error { calls++; return nil }); err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

// Attempts counts calls, the first included, however fast they fail and however much of Total is left.
func TestAttemptsLimitsCalls(t *testing.T) {
	want := errors.New("503")
	calls, retries := 0, 0
	p := Policy{Total: time.Minute, Initial: time.Millisecond, Max: time.Millisecond, Attempts: 4, OnRetry: func(int, error, time.Duration) { retries++ }}
	err := Do(context.Background(), p, func(context.Context) error { calls++; return want })
	if !errors.Is(err, want) || calls != 4 || retries != 3 {
		t.Fatalf("err=%v calls=%d retries=%d, want %v after 4 calls and 3 retries", err, calls, retries, want)
	}
	calls = 0
	if err := Do(context.Background(), Policy{Attempts: 4, Initial: time.Millisecond}, func(context.Context) error {
		calls++
		if calls < 4 {
			return want
		}
		return nil
	}); err != nil || calls != 4 {
		t.Fatalf("success on the last attempt: err=%v calls=%d", err, calls)
	}
}

// Jitter spreads each pause within ± the fraction of it, and the pause handed to OnRetry is the one waited.
func TestJitterSpreadsPauses(t *testing.T) {
	const base = 2 * time.Millisecond
	seen := map[time.Duration]bool{}
	p := Policy{Initial: base, Max: base, Attempts: 40, Jitter: 0.5, OnRetry: func(_ int, _ error, wait time.Duration) {
		if wait < base/2 || wait > base*3/2 {
			t.Errorf("pause %v outside %v ± 50%%", wait, base)
		}
		seen[wait] = true
	}}
	_ = Do(context.Background(), p, func(context.Context) error { return errors.New("down") })
	if len(seen) < 2 {
		t.Fatalf("40 pauses with jitter were all %v", seen)
	}
	// Without Jitter every pause is the configured one.
	p.Jitter, p.Attempts = 0, 5
	p.OnRetry = func(_ int, _ error, wait time.Duration) {
		if wait != base {
			t.Errorf("pause %v without jitter, want %v", wait, base)
		}
	}
	_ = Do(context.Background(), p, func(context.Context) error { return errors.New("down") })
}

// A negative Total sets no time limit: a pause longer than the default 60 s is still waited, and only ctx ends it.
func TestNegativeTotalHasNoTimeLimit(t *testing.T) {
	want := errors.New("down")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	retried := false
	err := Do(ctx, Policy{Total: -1, Initial: 2 * time.Minute, Max: 2 * time.Minute, OnRetry: func(int, error, time.Duration) {
		retried = true
		cancel()
	}}, func(context.Context) error { return want })
	if !errors.Is(err, want) || !retried {
		t.Fatalf("err=%v retried=%v: a 2 min pause must be waited when Total < 0", err, retried)
	}
	// With a time limit the same pause is not started at all.
	retried = false
	err = Do(context.Background(), Policy{Total: time.Second, Initial: 2 * time.Minute, OnRetry: func(int, error, time.Duration) { retried = true }}, func(context.Context) error { return want })
	if !errors.Is(err, want) || retried {
		t.Fatalf("err=%v retried=%v: a pause beyond Total must end it", err, retried)
	}
}
