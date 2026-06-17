package breaker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/redis/go-redis/v9"
)

// fakeClock is an injectable, advanceable clock (SPEC §14.1).
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newBreaker(t *testing.T) (*Breaker, *fakeClock, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	b, err := NewWithClock(context.Background(), rdb, clk, nil)
	if err != nil {
		t.Fatalf("breaker: %v", err)
	}
	return b, clk, mr
}

var testCfg = Config{FailureThreshold: 3, WindowS: 30, CooldownS: 15, HalfOpenMax: 1}

func TestBreaker_ClosedAllowsAndCountsFailures(t *testing.T) {
	b, _, _ := newBreaker(t)
	ctx := context.Background()
	p := "a"

	for i := 0; i < 3; i++ {
		if !b.Allow(ctx, p, testCfg) {
			t.Fatalf("attempt %d: closed breaker should allow", i)
		}
		b.Record(ctx, p, testCfg, adapter.ErrTimeout)
	}
	// Threshold reached -> open. Next Allow denies.
	if b.Allow(ctx, p, testCfg) {
		t.Fatalf("breaker should be open after 3 failures")
	}
	if b.StateOf(ctx, p) != StateOpen {
		t.Fatalf("state = %s want open", b.StateOf(ctx, p))
	}
}

func TestBreaker_RateLimitedAndBadPayloadDoNotTrip(t *testing.T) {
	b, _, _ := newBreaker(t)
	ctx := context.Background()
	p := "a"
	for i := 0; i < 10; i++ {
		b.Allow(ctx, p, testCfg)
		b.Record(ctx, p, testCfg, adapter.ErrRateLimited)
		b.Record(ctx, p, testCfg, adapter.ErrBadPayload)
	}
	if b.StateOf(ctx, p) != StateClosed {
		t.Fatalf("state = %s want closed (rate_limited/bad_payload must not trip)", b.StateOf(ctx, p))
	}
	if !b.Allow(ctx, p, testCfg) {
		t.Fatalf("breaker should still allow")
	}
}

func TestBreaker_OpenToHalfOpenAfterCooldown(t *testing.T) {
	b, clk, _ := newBreaker(t)
	ctx := context.Background()
	p := "a"

	// Trip it open.
	for i := 0; i < 3; i++ {
		b.Allow(ctx, p, testCfg)
		b.Record(ctx, p, testCfg, adapter.ErrUpstream)
	}
	if b.Allow(ctx, p, testCfg) {
		t.Fatalf("should be open")
	}
	// Before cooldown: still denied.
	clk.advance(14 * time.Second)
	if b.Allow(ctx, p, testCfg) {
		t.Fatalf("should still be open before cooldown")
	}
	// After cooldown: half-open admits one probe.
	clk.advance(2 * time.Second) // total 16s > 15s cooldown
	if !b.Allow(ctx, p, testCfg) {
		t.Fatalf("should admit probe after cooldown (half_open)")
	}
	if b.StateOf(ctx, p) != StateHalfOpen {
		t.Fatalf("state = %s want half_open", b.StateOf(ctx, p))
	}
	// A second concurrent probe is denied (half_open_max=1).
	if b.Allow(ctx, p, testCfg) {
		t.Fatalf("second probe should be denied while one is inflight")
	}
}

func TestBreaker_HalfOpenSuccessCloses(t *testing.T) {
	b, clk, _ := newBreaker(t)
	ctx := context.Background()
	p := "a"
	for i := 0; i < 3; i++ {
		b.Allow(ctx, p, testCfg)
		b.Record(ctx, p, testCfg, adapter.ErrTimeout)
	}
	clk.advance(16 * time.Second)
	if !b.Allow(ctx, p, testCfg) { // probe admitted
		t.Fatalf("probe should be admitted")
	}
	b.Record(ctx, p, testCfg, nil) // probe success
	if b.StateOf(ctx, p) != StateClosed {
		t.Fatalf("state = %s want closed after probe success", b.StateOf(ctx, p))
	}
	if !b.Allow(ctx, p, testCfg) {
		t.Fatalf("closed breaker should allow")
	}
}

func TestBreaker_HalfOpenFailureReopens(t *testing.T) {
	b, clk, _ := newBreaker(t)
	ctx := context.Background()
	p := "a"
	for i := 0; i < 3; i++ {
		b.Allow(ctx, p, testCfg)
		b.Record(ctx, p, testCfg, adapter.ErrTimeout)
	}
	clk.advance(16 * time.Second)
	if !b.Allow(ctx, p, testCfg) {
		t.Fatalf("probe should be admitted")
	}
	b.Record(ctx, p, testCfg, adapter.ErrUpstream) // probe fails
	if b.StateOf(ctx, p) != StateOpen {
		t.Fatalf("state = %s want open after probe failure", b.StateOf(ctx, p))
	}
	// Cooldown is reset: immediately after, still denied.
	if b.Allow(ctx, p, testCfg) {
		t.Fatalf("should be open with reset cooldown")
	}
	// After another cooldown, half-open again.
	clk.advance(16 * time.Second)
	if !b.Allow(ctx, p, testCfg) {
		t.Fatalf("should re-enter half_open after second cooldown")
	}
}

func TestBreaker_WindowSlideResetsFailures(t *testing.T) {
	b, clk, _ := newBreaker(t)
	ctx := context.Background()
	p := "a"
	// Two failures, then let the window elapse, then one more — should NOT open
	// (counter reset at window boundary).
	b.Allow(ctx, p, testCfg)
	b.Record(ctx, p, testCfg, adapter.ErrTimeout)
	b.Allow(ctx, p, testCfg)
	b.Record(ctx, p, testCfg, adapter.ErrTimeout)

	clk.advance(31 * time.Second) // window_s = 30

	b.Allow(ctx, p, testCfg)
	b.Record(ctx, p, testCfg, adapter.ErrTimeout)
	if b.StateOf(ctx, p) != StateClosed {
		t.Fatalf("state = %s want closed (window slid, count reset)", b.StateOf(ctx, p))
	}
}

func TestBreaker_FailOpenWhenRedisDown(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	logged := false
	b, _ := NewWithClock(context.Background(), rdb, &fakeClock{t: time.Now()}, func(string, string, error) { logged = true })

	mr.Close() // Redis goes away.

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if !b.Allow(ctx, "a", testCfg) {
		t.Fatalf("breaker must fail-open (allow) when Redis is down")
	}
	b.Record(ctx, "a", testCfg, adapter.ErrTimeout) // must not panic
	if !logged {
		t.Errorf("fail-open should log loudly")
	}
}

func TestClassify(t *testing.T) {
	if classify(nil) != outcomeSuccess {
		t.Errorf("nil -> success")
	}
	if classify(adapter.ErrTimeout) != outcomeFailure {
		t.Errorf("timeout -> failure")
	}
	if classify(adapter.ErrUpstream) != outcomeFailure {
		t.Errorf("upstream -> failure")
	}
	if classify(adapter.ErrRateLimited) != outcomeIgnore {
		t.Errorf("rate_limited -> ignore")
	}
	if classify(adapter.ErrBadPayload) != outcomeIgnore {
		t.Errorf("bad_payload -> ignore")
	}
	if classify(errors.New("weird")) != outcomeFailure {
		t.Errorf("unknown -> failure (conservative)")
	}
}
