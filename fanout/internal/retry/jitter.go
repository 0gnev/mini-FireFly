package retry

import (
	"math/rand"
	"sync"
	"time"
)

// FullJitter returns a production jitter source: a thread-safe math/rand
// generator producing a duration uniformly in [0, base). Randomness is
// injectable (this is the default; tests pass a deterministic Jitter).
func FullJitter() Jitter {
	var mu sync.Mutex
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return func(base time.Duration) time.Duration {
		if base <= 0 {
			return 0
		}
		mu.Lock()
		n := r.Int63n(int64(base))
		mu.Unlock()
		return time.Duration(n)
	}
}
