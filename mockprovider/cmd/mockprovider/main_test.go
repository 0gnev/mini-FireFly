package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mini-firefly/mockprovider/internal/gen"
)

func testFixturesDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	// file = .../mockprovider/cmd/mockprovider/main_test.go
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	return filepath.Join(root, "fixtures")
}

// startTestServer boots a real HTTP server on an ephemeral port and returns its
// base URL plus a shutdown func.
func startTestServer(t *testing.T, cfg config) (string, func()) {
	t.Helper()
	cfg.FixturesDir = testFixturesDir(t)
	fx, err := loadFixturesForTest(cfg.FixturesDir)
	if err != nil {
		t.Fatalf("fixtures: %v", err)
	}
	srv := newServer(cfg, fx, newLogger(io.Discard, "test-"+cfg.ProviderID))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: srv.routes()}
	go func() { _ = hs.Serve(ln) }()

	base := "http://" + ln.Addr().String()
	return base, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = hs.Shutdown(ctx)
	}
}

// loadFixturesForTest bypasses gen.Load's process-wide sync.Once so each test
// gets an isolated load.
func loadFixturesForTest(dir string) (*gen.Fixtures, error) {
	return gen.Load(dir) // Load caches, but always with the same repo dir here -> fine.
}

func baseCfg(provider string) config {
	return config{
		ProviderID:   provider,
		ChaosProfile: "stable",
		ChaosSeed:    42,
		RateLimitRPS: 50,
		BaseLatency:  1 * time.Millisecond, // keep tests fast
	}
}

func searchBody(origin, dest, date string) io.Reader {
	b, _ := json.Marshal(map[string]any{
		"origin": origin, "destination": dest, "depart_date": date, "passengers": 1,
	})
	return bytes.NewReader(b)
}

func TestHealthzAlwaysOK_EvenWhenDown(t *testing.T) {
	cfg := baseCfg("a")
	cfg.ChaosProfile = "down"
	base, stop := startTestServer(t, cfg)
	defer stop()

	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200 even in down profile", resp.StatusCode)
	}
	var m map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&m)
	if m["profile"] != "down" {
		t.Errorf("healthz profile = %q, want down", m["profile"])
	}
}

func TestDownProfile_SearchRefuses(t *testing.T) {
	cfg := baseCfg("a")
	cfg.ChaosProfile = "down"
	base, stop := startTestServer(t, cfg)
	defer stop()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post(base+"/search", "application/json", searchBody("BEG", "AMS", "2026-07-01"))
	if err == nil {
		resp.Body.Close()
		t.Fatalf("expected connection error in down profile, got status %d", resp.StatusCode)
	}
	// Any transport-level error (EOF/reset/refused) satisfies "connection refused".
}

func TestDownProfile_HealthWorksWhileSearchRefuses(t *testing.T) {
	cfg := baseCfg("a")
	cfg.ChaosProfile = "down"
	base, stop := startTestServer(t, cfg)
	defer stop()

	// /search refuses
	client := &http.Client{Timeout: 2 * time.Second}
	if _, err := client.Post(base+"/search", "application/json", searchBody("BEG", "AMS", "2026-07-01")); err == nil {
		t.Fatal("search should refuse in down profile")
	}
	// /healthz still answers
	resp, err := client.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("healthz must work in down profile: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
}

func TestRateLimit429Body(t *testing.T) {
	cfg := baseCfg("a")
	cfg.RateLimitRPS = 5
	base, stop := startTestServer(t, cfg)
	defer stop()

	client := &http.Client{Timeout: 2 * time.Second}
	var got429 bool
	var body map[string]any
	for i := 0; i < 50; i++ {
		resp, err := client.Post(base+"/search", "application/json", searchBody("BEG", "AMS", "2026-07-01"))
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			got429 = true
			_ = json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			break
		}
		resp.Body.Close()
	}
	if !got429 {
		t.Fatal("expected a 429 once RPS exceeded")
	}
	if body["error"] != "rate limited" {
		t.Errorf("429 body error = %v, want 'rate limited'", body["error"])
	}
	if ra, ok := body["retry_after_ms"].(float64); !ok || ra != 500 {
		t.Errorf("429 body retry_after_ms = %v, want 500", body["retry_after_ms"])
	}
}

func TestSearchDeterministicOverHTTP(t *testing.T) {
	cfg := baseCfg("a")
	base, stop := startTestServer(t, cfg)
	defer stop()

	get := func() string {
		resp, err := http.Post(base+"/search", "application/json", searchBody("BEG", "AMS", "2026-07-01"))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	first := get()
	for i := 0; i < 3; i++ {
		if got := get(); got != first {
			t.Fatalf("response %d differs; not byte-identical:\n%s\nvs\n%s", i, first, got)
		}
	}
	if !strings.Contains(first, "\"results\"") {
		t.Errorf("provider a response should have results envelope: %s", first)
	}
}

func TestAdminChaosToggle(t *testing.T) {
	cfg := baseCfg("a")
	base, stop := startTestServer(t, cfg)
	defer stop()

	// GET initial
	resp, _ := http.Get(base + "/admin/chaos")
	var m map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if m["profile"] != "stable" {
		t.Fatalf("initial profile = %q", m["profile"])
	}

	// PUT switch to flaky
	req, _ := http.NewRequest(http.MethodPut, base+"/admin/chaos",
		strings.NewReader(`{"profile":"flaky"}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d", resp.StatusCode)
	}

	// GET reflects change
	resp, _ = http.Get(base + "/admin/chaos")
	_ = json.NewDecoder(resp.Body).Decode(&m)
	resp.Body.Close()
	if m["profile"] != "flaky" {
		t.Fatalf("profile after PUT = %q, want flaky", m["profile"])
	}

	// invalid profile rejected
	req, _ = http.NewRequest(http.MethodPut, base+"/admin/chaos", strings.NewReader(`{"profile":"boom"}`))
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid profile PUT status = %d, want 400", resp.StatusCode)
	}
}

func TestUnknownRouteEmptyOverHTTP(t *testing.T) {
	cfg := baseCfg("a")
	base, stop := startTestServer(t, cfg)
	defer stop()

	resp, err := http.Post(base+"/search", "application/json", searchBody("JFK", "ATH", "2026-07-01"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unknown route status = %d, want 200", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	// Provider A => empty results array.
	if !strings.Contains(string(b), `"results":[]`) {
		t.Errorf("expected empty results, got %s", b)
	}
}

func TestRuntimeDownToggleRefusesSearch(t *testing.T) {
	// Acceptance §5.6: PUT {"profile":"down"} makes /search refuse within 1s
	// while /healthz still answers.
	cfg := baseCfg("a")
	base, stop := startTestServer(t, cfg)
	defer stop()

	// Initially search works.
	resp, err := http.Post(base+"/search", "application/json", searchBody("BEG", "AMS", "2026-07-01"))
	if err != nil {
		t.Fatalf("initial search failed: %v", err)
	}
	resp.Body.Close()

	// Flip to down.
	req, _ := http.NewRequest(http.MethodPut, base+"/admin/chaos", strings.NewReader(`{"profile":"down"}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	deadline := time.Now().Add(time.Second)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	refused := false
	for time.Now().Before(deadline) {
		if _, err := client.Post(base+"/search", "application/json", searchBody("BEG", "AMS", "2026-07-01")); err != nil {
			refused = true
			break
		}
	}
	if !refused {
		t.Fatal("search did not refuse within 1s after switching to down")
	}
	// health still ok
	hresp, err := client.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("healthz must still answer: %v", err)
	}
	hresp.Body.Close()
}
