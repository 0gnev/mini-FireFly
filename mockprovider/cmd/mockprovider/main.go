// Command mockprovider is a single-binary mock flight-data provider whose
// behavior is selected entirely by environment variables (SPEC §5).
//
// Endpoints:
//
//	POST /search        format varies per PROVIDER_ID (§5.5)
//	GET  /healthz       200 even in `down` profile
//	PUT  /admin/chaos   switch profile at runtime
//	GET  /admin/chaos   current profile
//
// Fixtures: airports.json and routes.json are read at startup from
// FIXTURES_DIR (default /fixtures, where the Docker image bakes them).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mini-firefly/mockprovider/internal/chaos"
	"github.com/mini-firefly/mockprovider/internal/formats"
	"github.com/mini-firefly/mockprovider/internal/gen"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(2)
	}

	logger := newLogger(os.Stdout, "mockprovider-"+cfg.ProviderID)

	fx, err := gen.Load(cfg.FixturesDir)
	if err != nil {
		logger.log("fatal", "", fmt.Sprintf("load fixtures: %v", err), nil)
		os.Exit(1)
	}

	srv := newServer(cfg, fx, logger)

	httpServer := &http.Server{
		Addr:    ":" + strconv.Itoa(cfg.Port),
		Handler: srv.routes(),
	}

	// Graceful shutdown on SIGTERM/SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go func() {
		logger.log("info", "", fmt.Sprintf("listening on :%d profile=%s", cfg.Port, srv.chaos.Profile()), map[string]any{
			"provider_id": cfg.ProviderID,
		})
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.log("fatal", "", fmt.Sprintf("listen: %v", err), nil)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.log("info", "", "shutting down", nil)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
}

// ---- config ----------------------------------------------------------------

type config struct {
	ProviderID   string
	Port         int
	ChaosProfile string
	ChaosSeed    int64
	RateLimitRPS int
	BaseLatency  time.Duration
	FixturesDir  string
}

func loadConfig() (config, error) {
	c := config{
		Port:         8080,
		ChaosProfile: "stable",
		ChaosSeed:    42,
		RateLimitRPS: 50,
		BaseLatency:  80 * time.Millisecond,
		FixturesDir:  gen.DefaultDir(),
	}

	c.ProviderID = strings.ToLower(strings.TrimSpace(os.Getenv("PROVIDER_ID")))
	switch c.ProviderID {
	case "a", "b", "c", "d":
	case "":
		return c, errors.New("PROVIDER_ID is required (one of a/b/c/d)")
	default:
		return c, fmt.Errorf("PROVIDER_ID must be one of a/b/c/d, got %q", c.ProviderID)
	}

	if v := os.Getenv("PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil || p <= 0 || p > 65535 {
			return c, fmt.Errorf("invalid PORT %q", v)
		}
		c.Port = p
	}
	if v := os.Getenv("CHAOS_PROFILE"); v != "" {
		if !chaos.ValidProfile(v) {
			return c, fmt.Errorf("invalid CHAOS_PROFILE %q", v)
		}
		c.ChaosProfile = strings.ToLower(v)
	}
	if v := os.Getenv("CHAOS_SEED"); v != "" {
		s, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return c, fmt.Errorf("invalid CHAOS_SEED %q", v)
		}
		c.ChaosSeed = s
	}
	if v := os.Getenv("RATE_LIMIT_RPS"); v != "" {
		r, err := strconv.Atoi(v)
		if err != nil || r <= 0 {
			return c, fmt.Errorf("invalid RATE_LIMIT_RPS %q", v)
		}
		c.RateLimitRPS = r
	}
	if v := os.Getenv("BASE_LATENCY_MS"); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms < 0 {
			return c, fmt.Errorf("invalid BASE_LATENCY_MS %q", v)
		}
		c.BaseLatency = time.Duration(ms) * time.Millisecond
	}
	if v := os.Getenv("FIXTURES_DIR"); v != "" {
		c.FixturesDir = v
	}
	return c, nil
}

// ---- server ----------------------------------------------------------------

type server struct {
	cfg     config
	fx      *gen.Fixtures
	chaos   *chaos.Engine
	limiter *chaos.RateLimiter
	logger  *logger
}

func newServer(cfg config, fx *gen.Fixtures, lg *logger) *server {
	return &server{
		cfg:     cfg,
		fx:      fx,
		chaos:   chaos.NewEngine(chaos.Profile(cfg.ChaosProfile), cfg.ChaosSeed, cfg.BaseLatency),
		limiter: chaos.NewRateLimiter(cfg.RateLimitRPS),
		logger:  lg,
	}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/search", s.handleSearch)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/admin/chaos", s.handleAdminChaos)
	return mux
}

// requestID reads an incoming request id header if present, else mints one.
func requestID(r *http.Request) string {
	for _, h := range []string{"X-Request-Id", "X-Request-ID", "Request-Id"} {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req_unknown"
	}
	return "req_" + hex.EncodeToString(b[:])
}

func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	rid := requestID(r)
	// 200 even in `down` profile — health of the process, not the simulated provider.
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"profile": string(s.chaos.Profile()),
	})
	s.logger.log("info", rid, "healthz", map[string]any{"profile": string(s.chaos.Profile())})
}

func (s *server) handleAdminChaos(w http.ResponseWriter, r *http.Request) {
	rid := requestID(r)
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]string{"profile": string(s.chaos.Profile())})
	case http.MethodPut:
		var body struct {
			Profile string `json:"profile"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if err := s.chaos.SetProfile(body.Profile); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		s.logger.log("info", rid, "chaos profile changed", map[string]any{"profile": body.Profile})
		writeJSON(w, http.StatusOK, map[string]string{"profile": string(s.chaos.Profile())})
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	rid := requestID(r)

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	// Always-on hostility: rate limit regardless of profile.
	if !s.limiter.Allow() {
		w.Header().Set("Retry-After", "0")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":          "rate limited",
			"retry_after_ms": 500,
		})
		s.logger.log("warn", rid, "rate limited", nil)
		return
	}

	// Decide chaos for this request.
	d := s.chaos.Decide()

	if d.Action == chaos.ActionConnRefused {
		// down profile: hijack and close immediately without any response.
		s.hijackClose(w, rid, "down: connection refused")
		return
	}

	// Apply latency (respecting client disconnect).
	if d.Latency > 0 {
		select {
		case <-time.After(d.Latency):
		case <-r.Context().Done():
			s.logger.log("warn", rid, "client cancelled during latency", nil)
			return
		}
	}

	if d.Action == chaos.ActionReset {
		s.hijackClose(w, rid, "flaky: connection reset")
		return
	}

	if d.Action == chaos.ActionError500 {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		s.logger.log("warn", rid, "chaos 500", map[string]any{"profile": string(s.chaos.Profile())})
		return
	}

	// Parse request body (canonical Query). Tolerate empty body gracefully.
	var req formats.SearchRequest
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			s.logger.log("warn", rid, "bad request body", map[string]any{"err": err.Error()})
			return
		}
	}

	offers, err := gen.Generate(s.cfg.ProviderID, gen.Query{
		Origin:      req.Origin,
		Destination: req.Destination,
		DepartDate:  req.DepartDate,
		Passengers:  req.Passengers,
	}, s.fx)
	if err != nil {
		// Generation error (e.g. unparseable date) -> 400, not a chaos fault.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		s.logger.log("warn", rid, "generation error", map[string]any{"err": err.Error()})
		return
	}

	body, contentType, err := formats.Render(s.cfg.ProviderID, offers)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "render error"})
		s.logger.log("error", rid, "render error", map[string]any{"err": err.Error()})
		return
	}

	if d.Action == chaos.ActionTruncated {
		// Cut the body at a random byte > 10, Content-Type still JSON, status 200.
		cut := d.TruncateAt
		if cut > len(body) {
			cut = len(body)
		}
		if cut < 0 {
			cut = 0
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body[:cut])
		s.logger.log("warn", rid, "chaos truncated body", map[string]any{"cut_at": cut, "full_len": len(body)})
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	s.logger.log("info", rid, "search ok", map[string]any{
		"origin":      req.Origin,
		"destination": req.Destination,
		"offers":      len(offers),
	})
}

// hijackClose takes over the underlying connection and closes it immediately,
// simulating connection refused / reset.
func (s *server) hijackClose(w http.ResponseWriter, rid, reason string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		// Fallback: cannot hijack; emulate by aborting via a 500 without body.
		w.WriteHeader(http.StatusInternalServerError)
		s.logger.log("error", rid, "hijack unsupported; sent 500 instead", map[string]any{"reason": reason})
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		s.logger.log("error", rid, "hijack failed", map[string]any{"err": err.Error()})
		return
	}
	// Close immediately, no HTTP response written.
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0) // send RST rather than FIN, closer to a reset/refused
	}
	_ = conn.Close()
	s.logger.log("warn", rid, reason, nil)
}

// ---- helpers ---------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- structured logger -----------------------------------------------------

type logger struct {
	mu      sync.Mutex
	out     io.Writer
	service string
}

func newLogger(out io.Writer, service string) *logger {
	return &logger{out: out, service: service}
}

func (l *logger) log(level, requestID, msg string, fields map[string]any) {
	entry := map[string]any{
		"ts":      time.Now().UTC().Format(time.RFC3339Nano),
		"level":   level,
		"service": l.service,
		"msg":     msg,
	}
	if requestID != "" {
		entry["request_id"] = requestID
	}
	for k, v := range fields {
		entry[k] = v
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.out.Write(append(b, '\n'))
}
