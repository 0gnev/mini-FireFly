package chaos

import (
	"sync"
	"time"
)

// RateLimiter is an always-on sliding-window limiter (SPEC §5.3): more than
// `rps` requests within any rolling 1s window are rejected with 429.
type RateLimiter struct {
	mu     sync.Mutex
	rps    int
	window time.Duration
	events []time.Time
	nowFn  func() time.Time
}

// NewRateLimiter builds a limiter allowing rps requests per rolling second.
func NewRateLimiter(rps int) *RateLimiter {
	return &RateLimiter{
		rps:    rps,
		window: time.Second,
		nowFn:  time.Now,
	}
}

// Allow records an attempt and reports whether it is within the limit. When the
// limit is exceeded the attempt is NOT recorded (so a burst doesn't extend the
// block indefinitely beyond the window).
func (r *RateLimiter) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFn()
	cutoff := now.Add(-r.window)

	// Drop events older than the window.
	i := 0
	for ; i < len(r.events); i++ {
		if r.events[i].After(cutoff) {
			break
		}
	}
	if i > 0 {
		r.events = r.events[i:]
	}

	if len(r.events) >= r.rps {
		return false
	}
	r.events = append(r.events, now)
	return true
}
