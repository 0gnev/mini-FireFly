package bulkhead

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBulkhead_CapsConcurrency(t *testing.T) {
	bh := New(2)
	ctx := context.Background()

	r1, ok1 := bh.Acquire(ctx, "a")
	r2, ok2 := bh.Acquire(ctx, "a")
	if !ok1 || !ok2 {
		t.Fatalf("first two acquires should succeed")
	}

	// Third acquire blocks until a slot frees.
	done := make(chan struct{})
	go func() {
		r3, ok3 := bh.Acquire(ctx, "a")
		if ok3 {
			r3()
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatalf("third acquire should block while 2 slots are held")
	case <-time.After(50 * time.Millisecond):
	}
	r1() // free a slot
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("third acquire should proceed after a release")
	}
	r2()
}

func TestBulkhead_PerProviderIsolation(t *testing.T) {
	bh := New(1)
	ctx := context.Background()
	rA, okA := bh.Acquire(ctx, "a")
	if !okA {
		t.Fatalf("a acquire failed")
	}
	// b has its own slot.
	rB, okB := bh.Acquire(ctx, "b")
	if !okB {
		t.Fatalf("b should not be blocked by a")
	}
	rA()
	rB()
}

func TestBulkhead_CtxCancelUnblocks(t *testing.T) {
	bh := New(1)
	r, _ := bh.Acquire(context.Background(), "a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok := bh.Acquire(ctx, "a")
	if ok {
		t.Fatalf("acquire should fail when ctx already cancelled and slot full")
	}
	r()
}

func TestBulkhead_NeverExceedsMax(t *testing.T) {
	const max = 4
	bh := New(max)
	ctx := context.Background()
	var inside int32
	var maxSeen int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, ok := bh.Acquire(ctx, "a")
			if !ok {
				return
			}
			defer r()
			n := atomic.AddInt32(&inside, 1)
			for {
				m := atomic.LoadInt32(&maxSeen)
				if n <= m || atomic.CompareAndSwapInt32(&maxSeen, m, n) {
					break
				}
			}
			time.Sleep(time.Millisecond)
			atomic.AddInt32(&inside, -1)
		}()
	}
	wg.Wait()
	if maxSeen > max {
		t.Fatalf("observed %d concurrent, max is %d", maxSeen, max)
	}
}
