// Package limiter implements the per-provider Redis token bucket (SPEC §10.3).
//
// Bucket: capacity = rate_limit_rps, refill rate_limit_rps tokens/s, key
// rl:{provider}. Implemented as a Lua script invoked via EVALSHA. On an empty
// bucket, Allow returns false (report rate_limited immediately; do not queue).
//
// Purpose: protect the provider from us (client-side politeness), set slightly
// below the mock's own RATE_LIMIT_RPS so self-inflicted 429s don't poison the
// breaker.
//
// FAIL-OPEN: if Redis is unreachable, Allow returns true and logs loudly (SPEC
// §12.5) — the limiter must never be the reason a healthy provider is starved.
package limiter

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Clock is injectable for deterministic token-bucket math tests (SPEC §14.1).
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// RedisClient is the subset of redis ops the limiter needs.
type RedisClient interface {
	EvalSha(ctx context.Context, sha1 string, keys []string, args ...interface{}) *redis.Cmd
	ScriptLoad(ctx context.Context, script string) *redis.StringCmd
}

// FailLogger is called when the limiter fails open due to Redis being down.
type FailLogger func(provider string, err error)

// Limiter is the Redis-backed token bucket for all providers.
type Limiter struct {
	rdb     RedisClient
	clock   Clock
	logOpen FailLogger
	sha     string
}

// New constructs a Limiter, loading the Lua script. Lazy reload covers a
// Redis-down-at-boot case.
func New(ctx context.Context, rdb RedisClient, logOpen FailLogger) (*Limiter, error) {
	return NewWithClock(ctx, rdb, realClock{}, logOpen)
}

// NewWithClock is New with an injectable clock (tests).
func NewWithClock(ctx context.Context, rdb RedisClient, clock Clock, logOpen FailLogger) (*Limiter, error) {
	if logOpen == nil {
		logOpen = func(string, error) {}
	}
	l := &Limiter{rdb: rdb, clock: clock, logOpen: logOpen}
	_ = l.ensureScript(ctx)
	return l, nil
}

func (l *Limiter) ensureScript(ctx context.Context) error {
	if l.sha != "" {
		return nil
	}
	sha, err := l.rdb.ScriptLoad(ctx, bucketScript).Result()
	if err != nil {
		return err
	}
	l.sha = sha
	return nil
}

func key(provider string) string { return "rl:" + provider }

// Allow attempts to take one token from the provider's bucket. Returns true if
// a token was available; false if the bucket was empty (rate_limited). Fails
// OPEN (returns true) if Redis is unreachable.
func (l *Limiter) Allow(ctx context.Context, provider string, ratePerSec int) bool {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}
	if err := l.ensureScript(ctx); err != nil {
		l.logOpen(provider, err)
		return true // fail-open
	}
	nowMS := l.clock.Now().UnixMilli()
	res, err := l.rdb.EvalSha(ctx, l.sha, []string{key(provider)},
		ratePerSec, ratePerSec, nowMS).Result()
	if err != nil {
		if isNoScript(err) {
			l.sha = ""
			if err2 := l.ensureScript(ctx); err2 == nil {
				if res2, err3 := l.rdb.EvalSha(ctx, l.sha, []string{key(provider)},
					ratePerSec, ratePerSec, nowMS).Result(); err3 == nil {
					return toAllow(res2)
				}
			}
		}
		l.logOpen(provider, err)
		return true // fail-open
	}
	return toAllow(res)
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
	if err == nil || errors.Is(err, redis.Nil) {
		return false
	}
	msg := err.Error()
	return contains(msg, "NOSCRIPT") || contains(msg, "No matching script")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
