// Package bulkhead implements per-provider in-process concurrency isolation
// (SPEC §10.4): a semaphore (chan struct{}) caps concurrent calls to each
// provider at max_concurrent (BULKHEAD_MAX, default 8) so a melting-down
// provider cannot consume all fanout goroutine/socket capacity across
// overlapping requests.
package bulkhead

import (
	"context"
	"sync"
)

// Set holds one semaphore per provider, created lazily.
type Set struct {
	max  int
	mu   sync.Mutex
	sems map[string]chan struct{}
}

// New returns a bulkhead set with the given per-provider capacity. A non-
// positive max is clamped to 1.
func New(max int) *Set {
	if max < 1 {
		max = 1
	}
	return &Set{max: max, sems: make(map[string]chan struct{})}
}

func (s *Set) sem(provider string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.sems[provider]
	if !ok {
		ch = make(chan struct{}, s.max)
		s.sems[provider] = ch
	}
	return ch
}

// Acquire takes a slot for the provider. It blocks until a slot is free or ctx
// is done. It returns a release func and ok=true on success; ok=false (with a
// no-op release) if ctx was cancelled before a slot opened.
func (s *Set) Acquire(ctx context.Context, provider string) (release func(), ok bool) {
	ch := s.sem(provider)
	select {
	case ch <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-ch }) }, true
	case <-ctx.Done():
		return func() {}, false
	}
}
