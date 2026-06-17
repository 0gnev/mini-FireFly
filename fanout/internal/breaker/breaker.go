// Package breaker implements the per-provider circuit breaker (SPEC §10.1).
//
// State machine: closed -> open -> half_open -> closed. State lives in a Redis
// hash breaker:{provider} = {state, failures, opened_at, probe_inflight}, and
// all transitions run through Lua scripts so two fanout replicas cannot both
// send a half-open probe (atomicity requirement, SPEC §10.1/§11.2).
//
//   - closed: count failures in a sliding window (window_s). Failures =
//     ErrTimeout, ErrUpstream, connection errors. ErrRateLimited and
//     ErrBadPayload do NOT trip the breaker. At failure_threshold -> open.
//   - open: short-circuit to breaker_open. After cooldown_s -> half_open.
//   - half_open: admit half_open_max probes. Success -> closed (reset). Failure
//     -> open (reset cooldown).
//
// FAIL-OPEN: if Redis is unreachable, Allow returns true and Record is a no-op,
// and the event is logged loudly (SPEC §12.5). The breaker must never be the
// reason a provider can't be reached.
package breaker

import (
	"context"
	"errors"
	"time"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/redis/go-redis/v9"
)

// State is the breaker state as a string (mirrors the Redis hash field).
type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half_open"
)

// Gauge values for the Prometheus fanout_breaker_state metric (SPEC §12.1):
// 0 closed / 1 half_open / 2 open.
func (s State) Gauge() float64 {
	switch s {
	case StateHalfOpen:
		return 1
	case StateOpen:
		return 2
	default:
		return 0
	}
}

// Clock is injectable so the state machine can be tested with a fake clock
// (SPEC §14.1).
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// RedisClient is the subset of redis operations the breaker needs. Both
// *redis.Client and miniredis-backed clients satisfy it.
type RedisClient interface {
	EvalSha(ctx context.Context, sha1 string, keys []string, args ...interface{}) *redis.Cmd
	ScriptLoad(ctx context.Context, script string) *redis.StringCmd
	HGet(ctx context.Context, key, field string) *redis.StringCmd
}

// FailLogger is called when the breaker fails open because Redis is
// unreachable, so the operator sees it loudly (SPEC §12.5).
type FailLogger func(provider, op string, err error)

// Config carries the per-provider breaker tuning for one Allow/Record cycle.
type Config struct {
	FailureThreshold int
	WindowS          int
	CooldownS        int
	HalfOpenMax      int
}

func (c Config) withDefaults() Config {
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = 5
	}
	if c.WindowS <= 0 {
		c.WindowS = 30
	}
	if c.CooldownS <= 0 {
		c.CooldownS = 15
	}
	if c.HalfOpenMax <= 0 {
		c.HalfOpenMax = 1
	}
	return c
}

// Breaker is the Redis-backed circuit breaker for all providers.
type Breaker struct {
	rdb       RedisClient
	clock     Clock
	logOpen   FailLogger
	allowSHA  string
	recordSHA string
}

// New constructs a Breaker, loading the Lua scripts into Redis. If script
// loading fails (Redis down), the scripts are loaded lazily on first use and
// the breaker fails open until Redis returns.
func New(ctx context.Context, rdb RedisClient, logOpen FailLogger) (*Breaker, error) {
	return NewWithClock(ctx, rdb, realClock{}, logOpen)
}

// NewWithClock is New with an injectable clock (tests).
func NewWithClock(ctx context.Context, rdb RedisClient, clock Clock, logOpen FailLogger) (*Breaker, error) {
	if logOpen == nil {
		logOpen = func(string, string, error) {}
	}
	b := &Breaker{rdb: rdb, clock: clock, logOpen: logOpen}
	// Best-effort eager load; lazy load covers the Redis-down-at-boot case.
	_ = b.ensureScripts(ctx)
	return b, nil
}

func (b *Breaker) ensureScripts(ctx context.Context) error {
	if b.allowSHA != "" && b.recordSHA != "" {
		return nil
	}
	if b.allowSHA == "" {
		sha, err := b.rdb.ScriptLoad(ctx, allowScript).Result()
		if err != nil {
			return err
		}
		b.allowSHA = sha
	}
	if b.recordSHA == "" {
		sha, err := b.rdb.ScriptLoad(ctx, recordScript).Result()
		if err != nil {
			return err
		}
		b.recordSHA = sha
	}
	return nil
}

func key(provider string) string { return "breaker:" + provider }

// Allow decides whether a call to the provider may proceed. It runs the atomic
// allow script, which also performs the open->half_open cooldown transition and
// reserves a probe slot in half_open. Fails OPEN (returns true) if Redis is
// unreachable.
func (b *Breaker) Allow(ctx context.Context, provider string, cfg Config) bool {
	cfg = cfg.withDefaults()
	if err := b.ensureScripts(ctx); err != nil {
		b.logOpen(provider, "allow.load", err)
		return true // fail-open
	}
	now := b.clock.Now().UnixMilli()
	res, err := b.rdb.EvalSha(ctx, b.allowSHA, []string{key(provider)},
		now, cfg.CooldownS, cfg.HalfOpenMax, cfg.WindowS).Result()
	if err != nil {
		if isNoScript(err) {
			b.allowSHA = ""
			if err2 := b.ensureScripts(ctx); err2 == nil {
				if res2, err3 := b.rdb.EvalSha(ctx, b.allowSHA, []string{key(provider)},
					now, cfg.CooldownS, cfg.HalfOpenMax, cfg.WindowS).Result(); err3 == nil {
					return toAllow(res2)
				}
			}
		}
		b.logOpen(provider, "allow", err)
		return true // fail-open
	}
	return toAllow(res)
}

// Record reports the outcome of a call so the breaker can update its state.
// errClass: nil for success; otherwise the (sentinel) error. ErrRateLimited and
// ErrBadPayload are ignored (do not trip). Fails open (no-op) if Redis is down.
func (b *Breaker) Record(ctx context.Context, provider string, cfg Config, callErr error) {
	cfg = cfg.withDefaults()

	outcome := classify(callErr)
	if outcome == outcomeIgnore {
		// Still let the script observe a "neutral" probe completion so a
		// half-open probe slot is released even on ignored errors.
		outcome = outcomeNeutral
	}

	if err := b.ensureScripts(ctx); err != nil {
		b.logOpen(provider, "record.load", err)
		return // fail-open
	}
	now := b.clock.Now().UnixMilli()
	_, err := b.rdb.EvalSha(ctx, b.recordSHA, []string{key(provider)},
		now, int(outcome), cfg.FailureThreshold, cfg.WindowS, cfg.CooldownS).Result()
	if err != nil {
		if isNoScript(err) {
			b.recordSHA = ""
			if err2 := b.ensureScripts(ctx); err2 == nil {
				if _, err3 := b.rdb.EvalSha(ctx, b.recordSHA, []string{key(provider)},
					now, int(outcome), cfg.FailureThreshold, cfg.WindowS, cfg.CooldownS).Result(); err3 == nil {
					return
				}
			}
		}
		b.logOpen(provider, "record", err)
	}
}

// StateOf reads the current breaker state for metrics/health. Returns
// StateClosed if Redis is unreachable or no state exists yet.
func (b *Breaker) StateOf(ctx context.Context, provider string) State {
	v, err := b.rdb.HGet(ctx, key(provider), "state").Result()
	if err != nil || v == "" {
		return StateClosed
	}
	return State(v)
}

type outcome int

const (
	outcomeSuccess outcome = 0
	outcomeFailure outcome = 1
	outcomeNeutral outcome = 2 // ignored error class; release probe, no count
	outcomeIgnore  outcome = 3 // internal marker before remap
)

// classify maps a call error to a breaker outcome. Only ErrTimeout/ErrUpstream
// (and connection errors, which the HTTP layer maps to ErrTimeout) count as
// failures. ErrRateLimited and ErrBadPayload are ignored.
func classify(callErr error) outcome {
	if callErr == nil {
		return outcomeSuccess
	}
	if errors.Is(callErr, adapter.ErrTimeout) || errors.Is(callErr, adapter.ErrUpstream) {
		return outcomeFailure
	}
	if errors.Is(callErr, adapter.ErrRateLimited) || errors.Is(callErr, adapter.ErrBadPayload) {
		return outcomeIgnore
	}
	// Unknown error: be conservative and count it as a failure (it is a
	// provider-down signal of some kind).
	return outcomeFailure
}

func toAllow(res interface{}) bool {
	switch v := res.(type) {
	case int64:
		return v == 1
	case int:
		return v == 1
	default:
		return false
	}
}

func isNoScript(err error) bool {
	if err == nil {
		return false
	}
	return !errors.Is(err, redis.Nil) &&
		(contains(err.Error(), "NOSCRIPT") || contains(err.Error(), "No matching script"))
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
