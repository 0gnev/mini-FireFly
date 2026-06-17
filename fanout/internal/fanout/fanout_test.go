package fanout

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/mini-firefly/fanout/internal/breaker"
	"github.com/mini-firefly/fanout/internal/model"
)

// --- fakes ---

type fakeBreaker struct {
	allow   map[string]bool
	records int32
}

func (f *fakeBreaker) Allow(_ context.Context, p string, _ breaker.Config) bool {
	if f.allow == nil {
		return true
	}
	v, ok := f.allow[p]
	return !ok || v
}
func (f *fakeBreaker) Record(_ context.Context, _ string, _ breaker.Config, _ error) {
	atomic.AddInt32(&f.records, 1)
}
func (f *fakeBreaker) StateOf(_ context.Context, _ string) breaker.State { return breaker.StateClosed }

type fakeLimiter struct{ deny map[string]bool }

func (f *fakeLimiter) Allow(_ context.Context, p string, _ int) bool {
	return f.deny == nil || !f.deny[p]
}

type fakeBulkhead struct{ full map[string]bool }

func (f *fakeBulkhead) Acquire(_ context.Context, p string) (func(), bool) {
	if f.full != nil && f.full[p] {
		return func() {}, false
	}
	return func() {}, true
}

// stubAdapter returns canned offers/err after a delay, honoring ctx.
type stubAdapter struct {
	name   string
	delay  time.Duration
	offers []model.Offer
	err    error
	calls  *int32
}

func (s *stubAdapter) Name() string { return s.name }
func (s *stubAdapter) Search(ctx context.Context, _ model.Query) ([]model.Offer, error) {
	if s.calls != nil {
		atomic.AddInt32(s.calls, 1)
	}
	if s.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, adapter.ErrTimeout
		case <-time.After(s.delay):
		}
	}
	return s.offers, s.err
}

func registryOf(adapters map[string]adapter.Adapter) adapter.Registry {
	r := adapter.Registry{}
	for name, ad := range adapters {
		ad := ad
		r[name] = func(_ adapter.ProviderConfig) adapter.Adapter { return ad }
	}
	return r
}

func newService(reg adapter.Registry, brk Breaker, lim Limiter, bh Bulkhead) *Service {
	return New(Config{
		Registry:    reg,
		Breakers:    brk,
		Limiters:    lim,
		Bulkheads:   bh,
		MaxAttempts: 2,
		BaseDelay:   1 * time.Millisecond,
		Jitter:      func(time.Duration) time.Duration { return 0 },
	})
}

func cfgs(names ...string) []adapter.ProviderConfig {
	out := make([]adapter.ProviderConfig, 0, len(names))
	for _, n := range names {
		out = append(out, adapter.ProviderConfig{Name: n, BaseURL: "http://x", TimeoutMS: 500})
	}
	return out
}

func resultByProvider(rs []model.ProviderResult) map[string]model.ProviderResult {
	m := make(map[string]model.ProviderResult, len(rs))
	for _, r := range rs {
		m[r.Provider] = r
	}
	return m
}

// --- tests ---

func TestFanOut_AllOK(t *testing.T) {
	offers := []model.Offer{{OfferID: "a:1", Provider: "a", Price: "100.00", Currency: "EUR"}}
	reg := registryOf(map[string]adapter.Adapter{
		"a": &stubAdapter{name: "a", offers: offers},
		"b": &stubAdapter{name: "b", offers: offers},
	})
	svc := newService(reg, &fakeBreaker{}, &fakeLimiter{}, &fakeBulkhead{})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rs := svc.FanOut(ctx, "req1", model.Query{}, cfgs("a", "b"))
	if len(rs) != 2 {
		t.Fatalf("want 2 results, got %d", len(rs))
	}
	for _, r := range rs {
		if r.Status != model.StatusOK {
			t.Errorf("%s status = %s want ok", r.Provider, r.Status)
		}
	}
}

// SPEC §6.3 property 4 + §6.5: a slow provider past the deadline -> timeout,
// not omitted; fast providers still ok; never blocks past deadline.
func TestFanOut_DeadlinePartial(t *testing.T) {
	reg := registryOf(map[string]adapter.Adapter{
		"fast": &stubAdapter{name: "fast", offers: []model.Offer{{OfferID: "fast:1"}}},
		"slow": &stubAdapter{name: "slow", delay: 5 * time.Second},
	})
	svc := newService(reg, &fakeBreaker{}, &fakeLimiter{}, &fakeBulkhead{})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	rs := svc.FanOut(ctx, "req2", model.Query{}, cfgs("fast", "slow"))
	elapsed := time.Since(start)

	if elapsed > 300*time.Millisecond {
		t.Fatalf("FanOut blocked past deadline: %v", elapsed)
	}
	byP := resultByProvider(rs)
	if byP["fast"].Status != model.StatusOK {
		t.Errorf("fast status = %s want ok", byP["fast"].Status)
	}
	if byP["slow"].Status != model.StatusTimeout {
		t.Errorf("slow status = %s want timeout", byP["slow"].Status)
	}
	if len(rs) != 2 {
		t.Errorf("slow provider must be present as timeout, got %d results", len(rs))
	}
}

func TestFanOut_BreakerOpenShortCircuits(t *testing.T) {
	called := int32(0)
	reg := registryOf(map[string]adapter.Adapter{
		"a": &stubAdapter{name: "a", calls: &called, offers: []model.Offer{{OfferID: "a:1"}}},
	})
	brk := &fakeBreaker{allow: map[string]bool{"a": false}}
	svc := newService(reg, brk, &fakeLimiter{}, &fakeBulkhead{})

	rs := svc.FanOut(context.Background(), "req3", model.Query{}, cfgs("a"))
	if rs[0].Status != model.StatusBreakerOpen {
		t.Fatalf("status = %s want breaker_open", rs[0].Status)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Errorf("adapter should not be called when breaker is open")
	}
}

func TestFanOut_RateLimited(t *testing.T) {
	reg := registryOf(map[string]adapter.Adapter{
		"a": &stubAdapter{name: "a", offers: []model.Offer{{OfferID: "a:1"}}},
	})
	lim := &fakeLimiter{deny: map[string]bool{"a": true}}
	svc := newService(reg, &fakeBreaker{}, lim, &fakeBulkhead{})

	rs := svc.FanOut(context.Background(), "req4", model.Query{}, cfgs("a"))
	if rs[0].Status != model.StatusRateLimited {
		t.Fatalf("status = %s want rate_limited", rs[0].Status)
	}
}

func TestFanOut_BulkheadFull(t *testing.T) {
	reg := registryOf(map[string]adapter.Adapter{
		"a": &stubAdapter{name: "a", offers: []model.Offer{{OfferID: "a:1"}}},
	})
	bh := &fakeBulkhead{full: map[string]bool{"a": true}}
	svc := newService(reg, &fakeBreaker{}, &fakeLimiter{}, bh)

	rs := svc.FanOut(context.Background(), "req5", model.Query{}, cfgs("a"))
	if rs[0].Status != model.StatusError || rs[0].Error != "bulkhead full" {
		t.Fatalf("got %+v want error/bulkhead full", rs[0])
	}
}

func TestFanOut_BadPayloadStatusAndNoExtraRecord(t *testing.T) {
	reg := registryOf(map[string]adapter.Adapter{
		"a": &stubAdapter{name: "a", err: adapter.ErrBadPayload},
	})
	svc := newService(reg, &fakeBreaker{}, &fakeLimiter{}, &fakeBulkhead{})
	rs := svc.FanOut(context.Background(), "req6", model.Query{}, cfgs("a"))
	if rs[0].Status != model.StatusBadPayload {
		t.Fatalf("status = %s want bad_payload", rs[0].Status)
	}
	if rs[0].Attempts != 1 {
		t.Errorf("attempts = %d want 1 (bad_payload not retried)", rs[0].Attempts)
	}
}

func TestFanOut_UpstreamMapsToError(t *testing.T) {
	reg := registryOf(map[string]adapter.Adapter{
		"a": &stubAdapter{name: "a", err: adapter.ErrUpstream},
	})
	svc := newService(reg, &fakeBreaker{}, &fakeLimiter{}, &fakeBulkhead{})
	rs := svc.FanOut(context.Background(), "req7", model.Query{}, cfgs("a"))
	if rs[0].Status != model.StatusError {
		t.Fatalf("status = %s want error", rs[0].Status)
	}
	if rs[0].Attempts != 2 {
		t.Errorf("attempts = %d want 2 (upstream retried once)", rs[0].Attempts)
	}
}
