package chaos

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsUpToRPS(t *testing.T) {
	rl := NewRateLimiter(50)
	base := time.Now()
	rl.nowFn = func() time.Time { return base }

	allowed := 0
	for i := 0; i < 100; i++ {
		if rl.Allow() {
			allowed++
		}
	}
	if allowed != 50 {
		t.Fatalf("expected exactly 50 allowed in a single instant, got %d", allowed)
	}
}

func TestRateLimiterSlidingWindow(t *testing.T) {
	rl := NewRateLimiter(10)
	now := time.Now()
	rl.nowFn = func() time.Time { return now }

	// Fill the bucket.
	for i := 0; i < 10; i++ {
		if !rl.Allow() {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	if rl.Allow() {
		t.Fatal("11th request in same window should be rejected")
	}

	// Advance past the window: old events expire, new ones allowed.
	now = now.Add(1100 * time.Millisecond)
	if !rl.Allow() {
		t.Fatal("request after window should be allowed again")
	}
}
