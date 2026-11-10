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
