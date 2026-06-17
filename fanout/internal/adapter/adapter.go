// Package adapter defines the provider Adapter contract (SPEC §6.2): the
// interface a provider integration must satisfy, the sentinel errors the
// fan-out switches behavior on, the per-request ProviderConfig delivered by
// core, and the Registry that maps a provider id to its constructor.
//
// Adding a provider == implement Adapter + add one Registry entry. Nothing else.
package adapter

import (
	"context"
	"errors"

	"github.com/mini-firefly/fanout/internal/model"
)

// Sentinel errors. The fan-out (retry, breaker, status mapping) switches
// behavior on these via errors.Is. Adapters MUST wrap their failures in one of
// these so the orchestration layer can reason about them.
var (
	// ErrRateLimited: provider said 429. No retry; report rate_limited; does
	// not trip the breaker.
	ErrRateLimited = errors.New("provider rate limited")
	// ErrBadPayload: malformed / truncated body. No retry; report bad_payload;
	// does not trip the breaker.
	ErrBadPayload = errors.New("malformed payload")
	// ErrTimeout: deadline exceeded / context cancelled mid-call. Retryable;
	// trips the breaker.
	ErrTimeout = errors.New("provider timeout")
	// ErrUpstream: provider 5xx. Retryable; trips the breaker.
	ErrUpstream = errors.New("provider 5xx")
)

// BreakerConfig is the per-provider breaker tuning (SPEC §9 / §10.1).
type BreakerConfig struct {
	FailureThreshold int `json:"failure_threshold"`
	WindowS          int `json:"window_s"`
	CooldownS        int `json:"cooldown_s"`
	HalfOpenMax      int `json:"half_open_max"`
}

// ProviderConfig is delivered per-request from core (SPEC §6.2 / §9). fanout
// holds no provider configuration of its own.
type ProviderConfig struct {
	Name         string        `json:"name"`
	BaseURL      string        `json:"base_url"`
	TimeoutMS    int           `json:"timeout_ms"`
	RateLimitRPS int           `json:"rate_limit_rps"`
	Breaker      BreakerConfig `json:"breaker"`
}

// Adapter performs a single provider search attempt. Retry, breaker, limiter,
// and bulkhead live OUTSIDE the adapter (SPEC §6.2).
type Adapter interface {
	Name() string
	// Search performs ONE attempt. Must respect ctx cancellation. Must wrap
	// failures in the sentinel errors above (errors.Is must work). Returns
	// offers already normalized to the canonical model.
	Search(ctx context.Context, q model.Query) ([]model.Offer, error)
}

// Registry maps a provider id to a constructor. Populated at wire-up.
type Registry map[string]func(cfg ProviderConfig) Adapter

// Build constructs the adapter for the given config. If no constructor is
// registered for cfg.Name, it returns nil and false.
func (r Registry) Build(cfg ProviderConfig) (Adapter, bool) {
	ctor, ok := r[cfg.Name]
	if !ok {
		return nil, false
	}
	return ctor(cfg), true
}
