// Package retry implements the fan-out retry policy (SPEC §10.2):
//
//   - Max attempts: 2 (1 initial + 1 retry).
//   - Retry only ErrTimeout, ErrUpstream, connection reset. Never ErrRateLimited
//     or ErrBadPayload.
//   - Backoff before retry: base 100ms × 2^(attempt-1) + full jitter [0, base).
//   - Skip the retry entirely if remaining ctx time < the per-attempt budget
//     (timeout_ms).
//   - Per-attempt timeout = min(provider.timeout_ms, remaining ctx) (SPEC §6.3
//     property 3), applied here so retry and the deadline cooperate.
//
// The jitter source is INJECTABLE so tests are deterministic (SPEC §19,
// §14.1 retry decision table).
package retry

import (
	"context"
	"errors"
	"time"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/mini-firefly/fanout/internal/model"
)

// Jitter returns a duration in [0, base). math/rand-backed in production,
// fixed in tests.
type Jitter func(base time.Duration) time.Duration

// Policy parameterizes Do.
type Policy struct {
	MaxAttempts int           // SPEC default 2
	BaseDelay   time.Duration // SPEC default 100ms
	Timeout     time.Duration // per-attempt budget = provider.timeout_ms
	Jitter      Jitter        // injectable; if nil, a no-jitter source is used
	// Sleep lets tests avoid real time. If nil, time.Sleep (ctx-aware) is used.
	Sleep func(ctx context.Context, d time.Duration)
}

// AttemptFunc performs a single attempt with the per-attempt context.
type AttemptFunc func(attemptCtx context.Context) ([]model.Offer, error)

// retryable reports whether err is in the retryable class (ErrTimeout,
// ErrUpstream; connection reset is mapped to ErrTimeout by the HTTP layer).
func retryable(err error) bool {
	return errors.Is(err, adapter.ErrTimeout) || errors.Is(err, adapter.ErrUpstream)
}

// Do runs fn under the retry policy. It returns the offers, the number of
// attempts actually made, and the final error (nil on success).
//
// Per-attempt context = min(remaining ctx, policy.Timeout). After a retryable
// failure, it backs off (base × 2^(attempt-1) + jitter) and retries only if the
// remaining ctx budget still covers another full attempt (timeout).
func Do(ctx context.Context, p Policy, fn AttemptFunc) ([]model.Offer, int, error) {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	jitter := p.Jitter
	if jitter == nil {
		jitter = func(time.Duration) time.Duration { return 0 }
	}
	sleep := p.Sleep
	if sleep == nil {
		sleep = ctxSleep
	}

	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= p.MaxAttempts; attempt++ {
		// Bail before starting an attempt if the deadline already passed.
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = adapter.ErrTimeout
			}
			return nil, attempts, lastErr
		}

		attemptCtx, cancel := perAttemptContext(ctx, p.Timeout)
		offers, err := fn(attemptCtx)
		cancel()
		attempts++

		if err == nil {
			return offers, attempts, nil
		}
		lastErr = err

		// Non-retryable errors stop immediately.
		if !retryable(err) {
			return nil, attempts, err
		}
		// No attempts left.
		if attempt == p.MaxAttempts {
			break
		}
		// Backoff = base × 2^(attempt-1) + full jitter [0, base of THIS step).
		step := p.BaseDelay << (attempt - 1) // base × 2^(attempt-1)
		backoff := step + jitter(step)

		// Skip the retry entirely if the remaining ctx budget can't cover the
		// backoff plus another full attempt (SPEC §10.2).
		remaining, ok := remainingBudget(ctx)
		if ok && remaining < backoff+p.Timeout {
			break
		}
		sleep(ctx, backoff)
	}
	return nil, attempts, lastErr
}

// perAttemptContext derives the per-attempt context, capping at the smaller of
// the provider timeout and the time remaining on ctx.
func perAttemptContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		// No explicit per-attempt cap; inherit the parent deadline.
		return context.WithCancel(ctx)
	}
	if dl, ok := ctx.Deadline(); ok {
		remaining := time.Until(dl)
		if remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		// Already expired; produce an immediately-cancelled context.
		c, cancel := context.WithCancel(ctx)
		cancel()
		return c, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// remainingBudget returns the time left on ctx and whether ctx has a deadline.
func remainingBudget(ctx context.Context) (time.Duration, bool) {
	if dl, ok := ctx.Deadline(); ok {
		return time.Until(dl), true
	}
	return 0, false
}

// ctxSleep sleeps for d but wakes early if ctx is cancelled.
func ctxSleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
