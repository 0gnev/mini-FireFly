package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/mini-firefly/fanout/internal/model"
)

// noJitter is a deterministic zero-jitter source.
func noJitter(time.Duration) time.Duration { return 0 }

// recordingSleep captures sleep durations instead of really sleeping.
type recordingSleep struct{ durs []time.Duration }

func (rs *recordingSleep) sleep(ctx context.Context, d time.Duration) { rs.durs = append(rs.durs, d) }

func basePolicy(rs *recordingSleep) Policy {
	return Policy{
		MaxAttempts: 2,
		BaseDelay:   100 * time.Millisecond,
		Timeout:     50 * time.Millisecond,
		Jitter:      noJitter,
		Sleep:       rs.sleep,
	}
}

func TestRetry_DecisionTable(t *testing.T) {
	cases := []struct {
		name         string
		errs         []error // error returned per attempt; nil = success
		wantAttempts int
		wantRetry    bool
		wantErrIs    error
	}{
		{"success first try", []error{nil}, 1, false, nil},
		{"timeout then success", []error{adapter.ErrTimeout, nil}, 2, true, nil},
		{"upstream then success", []error{adapter.ErrUpstream, nil}, 2, true, nil},
		{"rate_limited no retry", []error{adapter.ErrRateLimited}, 1, false, adapter.ErrRateLimited},
		{"bad_payload no retry", []error{adapter.ErrBadPayload}, 1, false, adapter.ErrBadPayload},
		{"timeout twice exhausts", []error{adapter.ErrTimeout, adapter.ErrTimeout}, 2, true, adapter.ErrTimeout},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := &recordingSleep{}
			// Generous deadline so the "skip retry if remaining < budget" branch
			// does not fire here.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			i := 0
			_, attempts, err := Do(ctx, basePolicy(rs), func(ctx context.Context) ([]model.Offer, error) {
				e := c.errs[i]
				i++
				return nil, e
			})
			if attempts != c.wantAttempts {
				t.Errorf("attempts = %d want %d", attempts, c.wantAttempts)
			}
			didRetry := len(rs.durs) > 0
			if didRetry != c.wantRetry {
				t.Errorf("retry happened = %v want %v (sleeps=%v)", didRetry, c.wantRetry, rs.durs)
			}
			if c.wantErrIs != nil && !errors.Is(err, c.wantErrIs) {
				t.Errorf("err = %v want Is %v", err, c.wantErrIs)
			}
			if c.wantErrIs == nil && err != nil {
				t.Errorf("err = %v want nil", err)
			}
		})
	}
}

func TestRetry_BackoffValue(t *testing.T) {
	rs := &recordingSleep{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _ = Do(ctx, basePolicy(rs), func(ctx context.Context) ([]model.Offer, error) {
		return nil, adapter.ErrTimeout
	})
	// First (and only) backoff: base × 2^0 + 0 jitter = 100ms.
	if len(rs.durs) != 1 || rs.durs[0] != 100*time.Millisecond {
		t.Errorf("backoff = %v want [100ms]", rs.durs)
	}
}

func TestRetry_SkipWhenRemainingTooSmall(t *testing.T) {
	rs := &recordingSleep{}
	p := basePolicy(rs)
	// Remaining budget is tiny: backoff(100ms)+timeout(50ms)=150ms needed, but
	// only ~60ms remains -> retry must be skipped.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, attempts, err := Do(ctx, p, func(ctx context.Context) ([]model.Offer, error) {
		return nil, adapter.ErrTimeout
	})
	if attempts != 1 {
		t.Errorf("attempts = %d want 1 (retry skipped)", attempts)
	}
	if len(rs.durs) != 0 {
		t.Errorf("should not have slept, got %v", rs.durs)
	}
	if !errors.Is(err, adapter.ErrTimeout) {
		t.Errorf("err = %v want ErrTimeout", err)
	}
}

func TestRetry_PerAttemptTimeoutCapped(t *testing.T) {
	// ctx deadline is 30ms; provider timeout is 50ms. Per-attempt context must
	// be capped at ~30ms (the remaining ctx), so a 200ms attempt is cancelled.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	rs := &recordingSleep{}
	start := time.Now()
	_, _, err := Do(ctx, basePolicy(rs), func(attemptCtx context.Context) ([]model.Offer, error) {
		select {
		case <-attemptCtx.Done():
			return nil, adapter.ErrTimeout
		case <-time.After(200 * time.Millisecond):
			return nil, nil
		}
	})
	if time.Since(start) > 120*time.Millisecond {
		t.Errorf("attempt not capped by remaining ctx, took %v", time.Since(start))
	}
	if !errors.Is(err, adapter.ErrTimeout) {
		t.Errorf("err = %v want ErrTimeout", err)
	}
}
