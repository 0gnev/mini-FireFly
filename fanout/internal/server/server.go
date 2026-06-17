// Package server exposes the fanout HTTP API (SPEC §6.4, §9):
//
//	POST /v1/fanout  internal fan-out endpoint
//	GET  /metrics    Prometheus exposition
//	GET  /healthz    Redis connectivity check (200/503)
//
// It also wires graceful shutdown on SIGTERM (drain <= 5s).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/mini-firefly/fanout/internal/logx"
	"github.com/mini-firefly/fanout/internal/model"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// FanOutRequest is the §9 request body.
type FanOutRequest struct {
	RequestID  string                   `json:"request_id"`
	DeadlineMS int                      `json:"deadline_ms"`
	Query      model.Query              `json:"query"`
	Providers  []adapter.ProviderConfig `json:"providers"`
}

// FanOutResponse is the §9 response body.
type FanOutResponse struct {
	RequestID string                 `json:"request_id"`
	Results   []model.ProviderResult `json:"results"`
}

// FanOuter is the orchestration dependency (the fanout.Service).
type FanOuter interface {
	FanOut(ctx context.Context, requestID string, q model.Query, provs []adapter.ProviderConfig) []model.ProviderResult
}

// RedisPinger checks Redis connectivity for /healthz.
type RedisPinger interface {
	Ping(ctx context.Context) error
}

// Server holds the HTTP handler dependencies.
type Server struct {
	svc   FanOuter
	redis RedisPinger
	reg   *prometheus.Registry
	log   *logx.Logger
}

// New builds a Server.
func New(svc FanOuter, redis RedisPinger, reg *prometheus.Registry, log *logx.Logger) *Server {
	return &Server{svc: svc, redis: redis, reg: reg, log: log}
}

// Handler returns the http.Handler for all routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/fanout", s.handleFanOut)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.Handle("/metrics", promhttp.HandlerFor(s.reg, promhttp.HandlerOpts{}))
	return mux
}

func (s *Server) handleFanOut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	var req FanOutRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		s.log.Warn("", "", "bad fanout request body", logx.Fields{"error": err.Error()})
		writeError(w, http.StatusBadRequest, "invalid_json")
		return
	}
	if req.DeadlineMS <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_deadline")
		return
	}

	// One deadline authority: derive the request context from deadline_ms.
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.DeadlineMS)*time.Millisecond)
	defer cancel()

	s.log.Info(req.RequestID, "", "fanout received", logx.Fields{
		"deadline_ms": req.DeadlineMS,
		"providers":   len(req.Providers),
		"origin":      req.Query.Origin,
		"destination": req.Query.Destination,
	})

	results := s.svc.FanOut(ctx, req.RequestID, req.Query, req.Providers)

	resp := FanOutResponse{RequestID: req.RequestID, Results: results}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		s.log.Error(req.RequestID, "", "encode fanout response", logx.Fields{"error": err.Error()})
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
	defer cancel()
	if s.redis != nil {
		if err := s.redis.Ping(ctx); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "unhealthy", "redis": err.Error()})
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func writeError(w http.ResponseWriter, code int, machine string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": machine})
}

// ErrServerClosed re-exports http.ErrServerClosed for callers checking shutdown.
var ErrServerClosed = http.ErrServerClosed

// IsServerClosed reports whether err is the benign post-Shutdown error.
func IsServerClosed(err error) bool { return errors.Is(err, http.ErrServerClosed) }
