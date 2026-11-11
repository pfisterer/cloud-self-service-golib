// Package loop runs a service's periodic work: a pass now or after a pause, then on every tick and on every nudge, until the context ends. A panic in a pass is recovered, logged with its stack and reported, and the loop goes on at its next tick: one broken pass must not end the work for the rest of the process's life, silently.
//
// It came from the AI service (package background: ticks, a first pause, a nudge and the recovery) and openstack-management-api (tree.RunLoop: two nudge channels, and a change announced before the first pass drained, since that pass covers it), whose loops had no recovery.
package loop

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"time"

	"go.uber.org/zap"
)

// ErrPanicked is what Pass returns, wrapped, for a pass that panicked.
var ErrPanicked = errors.New("pass panicked")

// Schedule says when a pass runs.
type Schedule struct {
	// Interval is the time between two ticks, counted from the start of Run. Zero or negative: no ticks, passes run only after First and on nudges.
	Interval time.Duration
	// First is the pause before the first pass; 0 = right away.
	First time.Duration
	// Nudges make a pass run at once whenever one of them receives; the regular ticks go on as before. A nil channel never receives, a closed one is dropped. A nudge during a pass is kept by the channel's buffer and starts the next pass when this one ends.
	Nudges []<-chan struct{}
}

// Options name the loop and say what happens on a panic.
type Options struct {
	// Name is the loop's name in log lines and for OnPanic's caller.
	Name string
	// Log receives a line with the stack for every recovered panic and a warning for every pass that returns an error; nil = no log lines.
	Log *zap.SugaredLogger
	// OnPanic, if set, is called after a recovered panic with the value and the stack, for a metric.
	OnPanic func(v any, stack []byte)
	// DrainBeforeFirst empties every nudge channel once before the first pass: whatever was announced before it is covered by it, so it does not cause a second pass right after.
	DrainBeforeFirst bool
}

// Run runs fn by s until ctx ends, and returns then; start it in a goroutine. A pass that returns an error is logged as a warning and the loop goes on; so it does after a panic, which is recovered, logged with its stack and handed to o.OnPanic.
func Run(ctx context.Context, s Schedule, fn func(context.Context) error, o Options) {
	var tick <-chan time.Time
	if s.Interval > 0 {
		t := time.NewTicker(s.Interval)
		defer t.Stop()
		tick = t.C
	}
	if s.First > 0 {
		first := time.NewTimer(s.First)
		select {
		case <-ctx.Done():
			first.Stop()
			return
		case <-first.C:
		}
	}
	if ctx.Err() != nil {
		return
	}
	if o.DrainBeforeFirst {
		for _, n := range s.Nudges {
			select {
			case <-n:
			default:
			}
		}
	}
	// ctx, the ticker and the nudges, in this order; reflect.Select because the number of nudges is not fixed.
	cases := make([]reflect.SelectCase, 0, 2+len(s.Nudges))
	cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(ctx.Done())})
	cases = append(cases, selectCase(tick))
	for _, n := range s.Nudges {
		cases = append(cases, selectCase(n))
	}
	for {
		if err := Pass(ctx, fn, o); err != nil && o.Log != nil && !errors.Is(err, ErrPanicked) {
			o.Log.Warnw("loop pass failed", "loop", o.Name, "error", err)
		}
		for {
			chosen, _, ok := reflect.Select(cases)
			if chosen == 0 {
				return
			}
			if ok || chosen == 1 {
				break
			}
			// A closed nudge: dropped, so it does not run passes back to back.
			cases[chosen].Chan = reflect.Value{}
		}
	}
}

// selectCase receives from c; a nil c never receives (a zero Chan is ignored by reflect.Select).
func selectCase[T any](c <-chan T) reflect.SelectCase {
	sc := reflect.SelectCase{Dir: reflect.SelectRecv}
	if c != nil {
		sc.Chan = reflect.ValueOf(c)
	}
	return sc
}

// Pass runs fn once with Run's recovery: a panic is logged with its stack (if o.Log is set), handed to o.OnPanic and returned as an error wrapping ErrPanicked. For a pass that runs outside the loop as well, such as one an endpoint triggers.
func Pass(ctx context.Context, fn func(context.Context) error, o Options) (err error) {
	defer func() {
		if r := recover(); r != nil {
			stack := debug.Stack()
			if o.Log != nil {
				o.Log.Errorw("loop pass panicked, going on at its next tick", "loop", o.Name, "panic", fmt.Sprint(r), "stack", string(stack))
			}
			if o.OnPanic != nil {
				o.OnPanic(r, stack)
			}
			err = fmt.Errorf("loop %s: %w: %v", o.Name, ErrPanicked, r)
		}
	}()
	return fn(ctx)
}
