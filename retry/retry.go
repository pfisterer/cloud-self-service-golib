// Package retry calls a function again with a growing pause until it succeeds or gives up.
//
// The services connect to Postgres, OIDC discovery and each other at startup. Since the NetworkPolicies, a new pod is let through only a moment after it starts, so the first attempt can fail with "connection refused" although nothing is wrong. Each service had grown its own loop for this, or none; this is the one answer to "try again for a while".
package retry

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// Policy is how long and how often to try. The zero value tries for 60 s with pauses from 250 ms doubling up to 5 s.
type Policy struct {
	// Total is how long to keep trying, counted from the first attempt; 0 = 60 s. A negative Total sets no time limit: only ctx, Attempts or a Permanent error end it, which is what a loop that must keep trying for as long as the process runs wants.
	Total time.Duration
	// Attempts is the most calls of fn there will be, the first included; 0 = no limit beyond Total. A step that must run a fixed number of times, however fast each call fails, says so here rather than deriving it from Total and the pauses.
	Attempts int
	// Jitter spreads each pause by up to ± this fraction of it (0.1 = ±10 %), so several pods that failed together do not retry in lockstep; 0 = none, values outside 0..1 are clamped. The doubling works on the pause without jitter.
	Jitter float64
	// Initial is the first pause; 0 = 250 ms. Each pause doubles up to Max.
	Initial time.Duration
	// Max is the longest pause; 0 = 5 s.
	Max time.Duration
	// OnRetry, if set, is called after a failed attempt that will be tried again, with the attempt's number (from 1), its error and the pause before the next one (with jitter applied, as it will be waited); for a log line.
	OnRetry func(attempt int, err error, wait time.Duration)
}

// permanent marks an error that is not tried again.
type permanent struct{ err error }

func (p permanent) Error() string { return p.err.Error() }
func (p permanent) Unwrap() error { return p.err }

// Permanent wraps an error that another attempt cannot fix (a wrong password, a malformed address): Do returns it at once, unwrapped.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanent{err}
}

// Do calls fn until it returns nil, returns an error wrapped by Permanent, ctx ends, Attempts calls have been made, or the next pause would end after Total. It returns nil on success, else the last error of fn (or ctx's error, if ctx ended before fn ever ran).
func Do(ctx context.Context, p Policy, fn func(ctx context.Context) error) error {
	total, wait, maxWait := p.Total, p.Initial, p.Max
	if total == 0 {
		total = 60 * time.Second
	}
	if wait <= 0 {
		wait = 250 * time.Millisecond
	}
	if maxWait <= 0 {
		maxWait = 5 * time.Second
	}
	var deadline time.Time
	if p.Total >= 0 {
		deadline = time.Now().Add(total)
	}
	jitter := min(max(p.Jitter, 0), 1)
	var last error
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			if last != nil {
				return last
			}
			return err
		}
		last = fn(ctx)
		if last == nil {
			return nil
		}
		if perm, ok := errors.AsType[permanent](last); ok {
			return perm.err
		}
		if p.Attempts > 0 && attempt >= p.Attempts {
			return last
		}
		pause := wait
		if jitter > 0 {
			pause += time.Duration((rand.Float64()*2 - 1) * jitter * float64(wait))
		}
		if !deadline.IsZero() && time.Now().Add(pause).After(deadline) {
			return last
		}
		if p.OnRetry != nil {
			p.OnRetry(attempt, last, pause)
		}
		t := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			t.Stop()
			return last
		case <-t.C:
		}
		wait = min(wait*2, maxWait)
	}
}
