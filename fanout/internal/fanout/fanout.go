// Package fanout implements the fan-out orchestration core (SPEC §6.3,
// normative). One goroutine per provider, each gated by breaker -> limiter ->
// bulkhead, then the adapter under the retry policy, with per-attempt timeout =
// min(provider.timeout_ms, remaining ctx). The collector loop returns whatever
// has arrived at the context deadline and never blocks past it; providers that
// never reported are filled in as `timeout`.
package fanout

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/mini-firefly/fanout/internal/breaker"
	"github.com/mini-firefly/fanout/internal/logx"
	"github.com/mini-firefly/fanout/internal/metrics"
	"github.com/mini-firefly/fanout/internal/model"
	"github.com/mini-firefly/fanout/internal/providers/normalize"
	"github.com/mini-firefly/fanout/internal/retry"
)

// Breaker is the per-provider circuit breaker dependency.
type Breaker interface {
	Allow(ctx context.Context, provider string, cfg breaker.Config) bool
	Record(ctx context.Context, provider string, cfg breaker.Config, callErr error)
	StateOf(ctx context.Context, provider string) breaker.State
}

// Limiter is the per-provider rate limiter dependency.
type Limiter interface {
	Allow(ctx context.Context, provider string, ratePerSec int) bool
}

// Bulkhead is the per-provider concurrency limiter dependency.
type Bulkhead interface {
	Acquire(ctx context.Context, provider string) (release func(), ok bool)
}

// Service orchestrates a fan-out across providers.
type Service struct {
	registry  adapter.Registry
	breakers  Breaker
	limiters  Limiter
	bulkheads Bulkhead
	metrics   *metrics.Metrics
	log       *logx.Logger

	// retry knobs from env (SPEC §13).
	maxAttempts int
	baseDelay   time.Duration
	jitter      retry.Jitter
}

// Config configures a Service.
type Config struct {
	Registry    adapter.Registry
	Breakers    Breaker
	Limiters    Limiter
	Bulkheads   Bulkhead
	Metrics     *metrics.Metrics
	Logger      *logx.Logger
	MaxAttempts int
	BaseDelay   time.Duration
	Jitter      retry.Jitter // injectable; defaults to full jitter
}

// New constructs a Service.
func New(cfg Config) *Service {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 2
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 100 * time.Millisecond
	}
	if cfg.Jitter == nil {
		cfg.Jitter = retry.FullJitter()
	}
	return &Service{
		registry:    cfg.Registry,
		breakers:    cfg.Breakers,
		limiters:    cfg.Limiters,
		bulkheads:   cfg.Bulkheads,
		metrics:     cfg.Metrics,
		log:         cfg.Logger,
		maxAttempts: cfg.MaxAttempts,
		baseDelay:   cfg.BaseDelay,
		jitter:      cfg.Jitter,
	}
}

// FanOut launches one goroutine per provider and collects results until the ctx
// deadline. The ctx already carries the request deadline set by the HTTP
// handler from deadline_ms (SPEC §9). request_id is used for log correlation.
//
// This is the normative algorithm of SPEC §6.3.
func (s *Service) FanOut(ctx context.Context, requestID string, q model.Query, provs []adapter.ProviderConfig) []model.ProviderResult {
	fanStart := time.Now()
	results := make(chan model.ProviderResult, len(provs))
	var wg sync.WaitGroup

	for _, p := range provs {
		wg.Add(1)
		go func(p adapter.ProviderConfig) {
			defer wg.Done()
			results <- s.callProvider(ctx, requestID, q, p)
		}(p)
	}

	go func() { wg.Wait(); close(results) }()

	out := make([]model.ProviderResult, 0, len(provs))
	seen := make(map[string]bool, len(provs))
	collecting := true
	for collecting {
		select {
		case r, open := <-results:
			if !open {
				collecting = false
				break
			}
			out = append(out, r)
			seen[r.Provider] = true
		case <-ctx.Done():
			// Deadline reached: return what we have. Late goroutines write into
			// the buffered channel and unwind on their own (their HTTP calls
			// carry ctx), so nothing leaks.
			collecting = false
		}
	}

	// Providers that never reported by the deadline are `timeout`, not omitted.
	for _, p := range provs {
		if !seen[p.Name] {
			res := model.ProviderResult{Provider: p.Name, Status: model.StatusTimeout}
			out = append(out, res)
			s.observeResult(ctx, res)
		}
	}

	if s.metrics != nil {
		s.metrics.RequestDuration.Observe(time.Since(fanStart).Seconds())
	}
	return out
}

// callProvider runs the full per-provider pipeline: breaker -> limiter ->
// bulkhead -> adapter under retry. It returns a fully-shaped ProviderResult and
// records metrics/breaker outcome.
func (s *Service) callProvider(ctx context.Context, requestID string, q model.Query, p adapter.ProviderConfig) model.ProviderResult {
	start := time.Now()
	bcfg := breaker.Config{
		FailureThreshold: p.Breaker.FailureThreshold,
		WindowS:          p.Breaker.WindowS,
		CooldownS:        p.Breaker.CooldownS,
		HalfOpenMax:      p.Breaker.HalfOpenMax,
	}

	// 1. Breaker gate.
	if !s.breakers.Allow(ctx, p.Name, bcfg) {
		res := model.ProviderResult{Provider: p.Name, Status: model.StatusBreakerOpen, LatencyMS: 0, Attempts: 0}
		s.observeResult(ctx, res)
		s.logf(requestID, p.Name, "breaker open, short-circuit", nil)
		return res
	}

	// 2. Rate limiter gate.
	if !s.limiters.Allow(ctx, p.Name, p.RateLimitRPS) {
		res := model.ProviderResult{Provider: p.Name, Status: model.StatusRateLimited, LatencyMS: latencyMS(start), Attempts: 0}
		s.observeResult(ctx, res)
		s.logf(requestID, p.Name, "rate limited (client-side bucket empty)", nil)
		return res
	}

	// 3. Bulkhead slot.
	release, ok := s.bulkheads.Acquire(ctx, p.Name)
	if !ok {
		res := model.ProviderResult{Provider: p.Name, Status: model.StatusError, Error: "bulkhead full", LatencyMS: latencyMS(start), Attempts: 0}
		s.observeResult(ctx, res)
		s.logf(requestID, p.Name, "bulkhead full", nil)
		return res
	}
	defer release()

	// Build the adapter for this provider.
	ad, found := s.registry.Build(p)
	if !found {
		res := model.ProviderResult{Provider: p.Name, Status: model.StatusError, Error: "no adapter registered", LatencyMS: latencyMS(start), Attempts: 0}
		s.observeResult(ctx, res)
		s.logf(requestID, p.Name, "no adapter registered", nil)
		return res
	}

	// Track inflight gauge around the actual call.
	if s.metrics != nil {
		s.metrics.Inflight.WithLabelValues(p.Name).Inc()
		defer s.metrics.Inflight.WithLabelValues(p.Name).Dec()
	}

	// 4. Adapter call under retry policy.
	policy := retry.Policy{
		MaxAttempts: s.maxAttempts,
		BaseDelay:   s.baseDelay,
		Timeout:     time.Duration(p.TimeoutMS) * time.Millisecond,
		Jitter:      s.jitter,
	}
	offers, attempts, err := retry.Do(ctx, policy, func(attemptCtx context.Context) ([]model.Offer, error) {
		return ad.Search(attemptCtx, q)
	})

	// Record breaker outcome (ErrRateLimited/ErrBadPayload are ignored inside).
	s.breakers.Record(ctx, p.Name, bcfg, err)

	if attempts > 1 && s.metrics != nil {
		s.metrics.Retries.WithLabelValues(p.Name).Add(float64(attempts - 1))
	}

	res := toResult(p.Name, offers, attempts, err, time.Since(start))
	s.observeResult(ctx, res)
	if err != nil {
		s.logf(requestID, p.Name, "provider call failed: "+string(res.Status), logx.Fields{"attempts": attempts, "cause": failureCause(err), "error": res.Error})
	}
	return res
}

// observeResult records per-provider metrics and refreshes the breaker gauge.
func (s *Service) observeResult(ctx context.Context, r model.ProviderResult) {
	if s.metrics == nil {
		return
	}
	s.metrics.ProviderRequests.WithLabelValues(r.Provider, string(r.Status)).Inc()
	s.metrics.ProviderLatency.WithLabelValues(r.Provider).Observe(float64(r.LatencyMS) / 1000.0)
	if r.Status == model.StatusRateLimited {
		s.metrics.RateLimited.WithLabelValues(r.Provider).Inc()
	}
	if s.breakers != nil {
		st := s.breakers.StateOf(ctx, r.Provider)
		s.metrics.BreakerState.WithLabelValues(r.Provider).Set(st.Gauge())
	}
}

func (s *Service) logf(requestID, provider, msg string, extra logx.Fields) {
	if s.log == nil {
		return
	}
	s.log.Info(requestID, provider, msg, extra)
}

func latencyMS(start time.Time) int64 { return time.Since(start).Milliseconds() }

// toResult maps the adapter outcome to a ProviderResult, choosing the status
// from the sentinel error class and sanitizing the error text.
func toResult(provider string, offers []model.Offer, attempts int, err error, elapsed time.Duration) model.ProviderResult {
	res := model.ProviderResult{
		Provider:  provider,
		Attempts:  attempts,
		LatencyMS: elapsed.Milliseconds(),
	}
	if err == nil {
		res.Status = model.StatusOK
		res.Offers = offers
		return res
	}
	res.Status = statusForError(err)
	res.Error = normalize.Sanitize(err)
	return res
}

func statusForError(err error) model.ProviderStatus {
	switch {
	case isErr(err, adapter.ErrRateLimited):
		return model.StatusRateLimited
	case isErr(err, adapter.ErrBadPayload):
		return model.StatusBadPayload
	case isErr(err, adapter.ErrTimeout):
		return model.StatusTimeout
	case isErr(err, adapter.ErrUpstream):
		return model.StatusError
	default:
		return model.StatusError
	}
}

// failureCause categorizes a provider error for structured logs. The §4.3 status
// enum collapses dropped connections and deadline timeouts both into `timeout`,
// which hides whether a provider is DOWN (socket dropped) or merely SLOW. This
// finer cause is logged (not surfaced on the API) so operators can tell the two
// apart by grep instead of guessing from status alone.
func failureCause(err error) string {
	switch {
	case isErr(err, adapter.ErrRateLimited):
		return "rate_limited"
	case isErr(err, adapter.ErrBadPayload):
		return "bad_payload"
	case isErr(err, adapter.ErrUpstream):
		return "upstream" // 5xx or an unclassified transport failure (e.g. refused)
	case isErr(err, adapter.ErrTimeout):
		if strings.Contains(err.Error(), "connection reset") {
			return "connection_reset" // provider dropped the socket (down / flaky)
		}
		return "deadline_exceeded" // provider too slow for the request budget
	default:
		return "error"
	}
}
