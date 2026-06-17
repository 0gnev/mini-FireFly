package limiter

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newLimiter(t *testing.T) (*Limiter, *fakeClock, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	l, err := NewWithClock(context.Background(), rdb, clk, nil)
	if err != nil {
		t.Fatalf("limiter: %v", err)
	}
	return l, clk, mr
}

func TestLimiter_BucketStartsFullThenEmpties(t *testing.T) {
	l, _, _ := newLimiter(t)
	ctx := context.Background()
	// Capacity 5: first 5 succeed, 6th fails (no refill at same instant).
	for i := 0; i < 5; i++ {
		if !l.Allow(ctx, "a", 5) {
			t.Fatalf("token %d should be granted", i)
		}
	}
	if l.Allow(ctx, "a", 5) {
		t.Fatalf("6th token should be denied (bucket empty)")
	}
}

func TestLimiter_RefillOverTime(t *testing.T) {
	l, clk, _ := newLimiter(t)
	ctx := context.Background()
	// Drain capacity 10.
	for i := 0; i < 10; i++ {
		if !l.Allow(ctx, "a", 10) {
			t.Fatalf("token %d should be granted", i)
		}
	}
	if l.Allow(ctx, "a", 10) {
		t.Fatalf("should be empty")
	}
	// Advance 0.5s at 10 tokens/s -> 5 tokens refilled.
	clk.advance(500 * time.Millisecond)
	for i := 0; i < 5; i++ {
		if !l.Allow(ctx, "a", 10) {
			t.Fatalf("refilled token %d should be granted", i)
		}
	}
	if l.Allow(ctx, "a", 10) {
		t.Fatalf("6th refilled token should be denied")
	}
}

func TestLimiter_RefillCapsAtCapacity(t *testing.T) {
	l, clk, _ := newLimiter(t)
	ctx := context.Background()
	// Drain 3 of capacity 4.
	for i := 0; i < 3; i++ {
		l.Allow(ctx, "a", 4)
	}
	// Advance a long time; tokens should cap at capacity (4), not grow unbounded.
	clk.advance(1 * time.Hour)
	for i := 0; i < 4; i++ {
		if !l.Allow(ctx, "a", 4) {
			t.Fatalf("token %d should be granted (capped at capacity)", i)
		}
	}
	if l.Allow(ctx, "a", 4) {
		t.Fatalf("5th should be denied; bucket caps at capacity 4")
	}
}

func TestLimiter_PerProviderIsolation(t *testing.T) {
	l, _, _ := newLimiter(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		l.Allow(ctx, "a", 2)
	}
	if l.Allow(ctx, "a", 2) {
		t.Fatalf("a should be empty")
	}
	// b has its own bucket.
	if !l.Allow(ctx, "b", 2) {
		t.Fatalf("b should be independent and full")
	}
}

func TestLimiter_FailOpenWhenRedisDown(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	logged := false
	l, _ := NewWithClock(context.Background(), rdb, &fakeClock{t: time.Now()}, func(string, error) { logged = true })

	mr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if !l.Allow(ctx, "a", 5) {
		t.Fatalf("limiter must fail-open when Redis is down")
	}
	if !logged {
		t.Errorf("fail-open should log loudly")
	}
}
