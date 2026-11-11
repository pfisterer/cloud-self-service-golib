package loop

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func observed() (*zap.SugaredLogger, *observer.ObservedLogs) {
	core, logs := observer.New(zap.DebugLevel)
	return zap.New(core).Sugar(), logs
}

// start runs Run in a goroutine and returns a channel closed when it returns.
func start(ctx context.Context, s Schedule, fn func(context.Context) error, o Options) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, s, fn, o)
	}()
	return done
}

func wait(t *testing.T, c <-chan time.Time, what string) time.Time {
	t.Helper()
	select {
	case at := <-c:
		return at
	case <-time.After(2 * time.Second):
		t.Fatalf("no %s", what)
	}
	return time.Time{}
}

func quiet(t *testing.T, c <-chan time.Time, what string) {
	t.Helper()
	select {
	case <-c:
		t.Fatalf("a pass ran %s", what)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestGoesOnAfterPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log, logs := observed()
	var calls, panics atomic.Int32
	var gotStack atomic.Bool
	done := make(chan struct{})
	start(ctx, Schedule{Interval: 5 * time.Millisecond}, func(context.Context) error {
		switch calls.Add(1) {
		case 1:
			panic("boom")
		case 3:
			close(done)
		}
		return nil
	}, Options{Name: "test", Log: log, OnPanic: func(v any, stack []byte) {
		panics.Add(1)
		gotStack.Store(v == "boom" && len(stack) > 0)
	}})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("loop stopped after the panic: %d calls", calls.Load())
	}
	if panics.Load() != 1 || !gotStack.Load() {
		t.Fatalf("OnPanic: %d calls, value and stack %v", panics.Load(), gotStack.Load())
	}
	entries := logs.FilterMessage("loop pass panicked, going on at its next tick").All()
	if len(entries) != 1 || entries[0].ContextMap()["loop"] != "test" || entries[0].ContextMap()["stack"] == "" {
		t.Fatalf("panic log lines: %+v", entries)
	}
	if n := logs.FilterMessage("loop pass failed").Len(); n != 0 {
		t.Fatalf("a panic was also logged as a failed pass (%d)", n)
	}
}

func TestErrorIsLoggedAndLoopGoesOn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log, logs := observed()
	passes := make(chan time.Time, 10)
	start(ctx, Schedule{Interval: 5 * time.Millisecond}, func(context.Context) error {
		passes <- time.Now()
		return errors.New("down")
	}, Options{Name: "err", Log: log})
	wait(t, passes, "first pass")
	wait(t, passes, "second pass")
	if logs.FilterMessage("loop pass failed").Len() == 0 {
		t.Fatal("the error was not logged")
	}
}

func TestFirstDelayNudgesAndStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a, b := make(chan struct{}, 1), make(chan struct{}, 1)
	passes := make(chan time.Time, 10)
	begin := time.Now()
	done := start(ctx, Schedule{Interval: time.Hour, First: 30 * time.Millisecond, Nudges: []<-chan struct{}{a, nil, b}}, func(context.Context) error {
		passes <- time.Now()
		return nil
	}, Options{})
	if at := wait(t, passes, "first pass"); at.Sub(begin) < 30*time.Millisecond {
		t.Fatalf("first pass after %v, want at least First", at.Sub(begin))
	}
	a <- struct{}{}
	wait(t, passes, "pass on the first nudge")
	b <- struct{}{}
	wait(t, passes, "pass on the second nudge")
	quiet(t, passes, "without a nudge or a tick")
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return when ctx ended")
	}
}

func TestStopDuringFirstPause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	done := start(ctx, Schedule{Interval: time.Hour, First: time.Hour}, func(context.Context) error { calls.Add(1); return nil }, Options{})
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return during the first pause")
	}
	if calls.Load() != 0 {
		t.Fatal("ran before its first pause was over")
	}
}

func TestDrainBeforeFirst(t *testing.T) {
	for _, drain := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		changes := make(chan struct{}, 1)
		changes <- struct{}{}
		passes := make(chan time.Time, 10)
		start(ctx, Schedule{Interval: time.Hour, Nudges: []<-chan struct{}{changes}}, func(context.Context) error {
			passes <- time.Now()
			return nil
		}, Options{DrainBeforeFirst: drain})
		wait(t, passes, "first pass")
		if drain {
			quiet(t, passes, "again for a change the first pass covered")
		} else {
			wait(t, passes, "pass for the change announced before the first")
		}
		cancel()
	}
}

func TestClosedNudgeIsDropped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closed := make(chan struct{})
	close(closed)
	var calls atomic.Int32
	start(ctx, Schedule{Interval: time.Hour, Nudges: []<-chan struct{}{closed}}, func(context.Context) error { calls.Add(1); return nil }, Options{})
	time.Sleep(30 * time.Millisecond)
	if n := calls.Load(); n > 2 {
		t.Fatalf("a closed nudge ran %d passes", n)
	}
}

func TestNoIntervalRunsOnNudgesOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nudge := make(chan struct{}, 1)
	passes := make(chan time.Time, 10)
	start(ctx, Schedule{Nudges: []<-chan struct{}{nudge}}, func(context.Context) error { passes <- time.Now(); return nil }, Options{})
	wait(t, passes, "first pass")
	quiet(t, passes, "without a nudge")
	nudge <- struct{}{}
	wait(t, passes, "pass on the nudge")
}

func TestPass(t *testing.T) {
	var got any
	err := Pass(context.Background(), func(context.Context) error { panic("x") }, Options{Name: "p", OnPanic: func(v any, _ []byte) { got = v }})
	if !errors.Is(err, ErrPanicked) || got != "x" {
		t.Fatalf("Pass after a panic: %v, OnPanic got %v", err, got)
	}
	want := errors.New("e")
	if err := Pass(context.Background(), func(context.Context) error { return want }, Options{}); err != want {
		t.Fatalf("Pass returned %v, want the pass's error", err)
	}
}
