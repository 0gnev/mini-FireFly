// Command fanout is the Go fan-out service (SPEC §6). It serves the internal
// core->fanout API, fanning a search out to N providers in parallel under
// breakers, retries, rate limiting, and bulkheads, all bounded by a single
// request deadline.
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/mini-firefly/fanout/internal/breaker"
	"github.com/mini-firefly/fanout/internal/bulkhead"
	"github.com/mini-firefly/fanout/internal/fanout"
	"github.com/mini-firefly/fanout/internal/limiter"
	"github.com/mini-firefly/fanout/internal/logx"
	"github.com/mini-firefly/fanout/internal/metrics"
	"github.com/mini-firefly/fanout/internal/providers"
	"github.com/mini-firefly/fanout/internal/providers/normalize"
	"github.com/mini-firefly/fanout/internal/retry"
	"github.com/mini-firefly/fanout/internal/server"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/redis/go-redis/v9"
)

func main() {
	log := logx.New()

	cfg := loadConfig()
	log.Info("", "", "fanout starting", logx.Fields{
		"port": cfg.port, "redis_addr": cfg.redisAddr, "bulkhead_max": cfg.bulkheadMax,
	})

	fx, err := normalize.FXFromEnv(os.Getenv("FX_USD_EUR"), os.Getenv("FX_RSD_EUR"))
	if err != nil {
		log.Error("", "", "invalid FX config", logx.Fields{"error": err.Error()})
		os.Exit(1)
	}

	rdb := redis.NewClient(&redis.Options{Addr: cfg.redisAddr})

	bootCtx, bootCancel := context.WithTimeout(context.Background(), 3*time.Second)

	brk, _ := breaker.New(bootCtx, rdb, func(provider, op string, err error) {
		log.Error("", provider, "breaker fail-open: Redis unreachable", logx.Fields{"op": op, "error": err.Error()})
	})
	lim, _ := limiter.New(bootCtx, rdb, func(provider string, err error) {
		log.Error("", provider, "limiter fail-open: Redis unreachable", logx.Fields{"error": err.Error()})
	})
	// The boot context only bounds the breaker/limiter startup probes; release it
	// now (instead of deferring) so a later os.Exit on a fatal server error does
	// not skip the cancel — satisfies gocritic's exitAfterDefer and is cleaner.
	bootCancel()

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	mtr := metrics.New(reg)

	registry := providers.NewRegistry(fx)
	bh := bulkhead.New(cfg.bulkheadMax)

	svc := fanout.New(fanout.Config{
		Registry:    registry,
		Breakers:    brk,
		Limiters:    lim,
		Bulkheads:   bh,
		Metrics:     mtr,
		Logger:      log,
		MaxAttempts: cfg.retryMaxAttempts,
		BaseDelay:   time.Duration(cfg.retryBaseMS) * time.Millisecond,
		Jitter:      retry.FullJitter(),
	})

	srv := server.New(svc, redisPinger{rdb}, reg, log)

	httpSrv := &http.Server{
		Addr:              announceAddr(cfg.port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Graceful shutdown on SIGTERM/SIGINT: stop accepting, drain <= 5s.
	idleClosed := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
		<-sig
		log.Info("", "", "shutdown signal received, draining", nil)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Error("", "", "graceful shutdown error", logx.Fields{"error": err.Error()})
		}
		_ = rdb.Close()
		close(idleClosed)
	}()

	log.Info("", "", "fanout listening", logx.Fields{"addr": httpSrv.Addr})
	if err := httpSrv.ListenAndServe(); err != nil && !server.IsServerClosed(err) {
		log.Error("", "", "http server error", logx.Fields{"error": err.Error()})
		os.Exit(1)
	}
	<-idleClosed
	log.Info("", "", "fanout stopped", nil)
}

type config struct {
	port             string
	redisAddr        string
	bulkheadMax      int
	retryMaxAttempts int
	retryBaseMS      int
}

func loadConfig() config {
	return config{
		port:             getenv("PORT", "8090"),
		redisAddr:        getenv("REDIS_ADDR", "redis:6379"),
		bulkheadMax:      getenvInt("BULKHEAD_MAX", 8),
		retryMaxAttempts: getenvInt("RETRY_MAX_ATTEMPTS", 2),
		retryBaseMS:      getenvInt("RETRY_BASE_MS", 100),
	}
}

func announceAddr(port string) string { return ":" + port }

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// redisPinger adapts a *redis.Client to the server.RedisPinger interface.
type redisPinger struct{ rdb *redis.Client }

func (p redisPinger) Ping(ctx context.Context) error { return p.rdb.Ping(ctx).Err() }
