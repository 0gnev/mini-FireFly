package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/mini-firefly/fanout/internal/logx"
	"github.com/mini-firefly/fanout/internal/model"
	"github.com/prometheus/client_golang/prometheus"
)

type stubFanOuter struct {
	gotRequestID string
	gotProviders int
	hadDeadline  bool
}

func (s *stubFanOuter) FanOut(ctx context.Context, requestID string, q model.Query, provs []adapter.ProviderConfig) []model.ProviderResult {
	s.gotRequestID = requestID
	s.gotProviders = len(provs)
	_, s.hadDeadline = ctx.Deadline()
	out := make([]model.ProviderResult, 0, len(provs))
	for _, p := range provs {
		out = append(out, model.ProviderResult{Provider: p.Name, Status: model.StatusOK})
	}
	return out
}

type stubPinger struct{ err error }

func (p stubPinger) Ping(context.Context) error { return p.err }

func newTestServer(svc FanOuter, pinger RedisPinger) *Server {
	reg := prometheus.NewRegistry()
	return New(svc, pinger, reg, logx.NewWith(&bytes.Buffer{}, nil))
}

func TestFanOutHandler_DerivesDeadlineAndEchoesRequestID(t *testing.T) {
	stub := &stubFanOuter{}
	srv := newTestServer(stub, stubPinger{})

	body := FanOutRequest{
		RequestID:  "req_abc",
		DeadlineMS: 1850,
		Query:      model.Query{Origin: "BEG", Destination: "AMS"},
		Providers: []adapter.ProviderConfig{
			{Name: "a", BaseURL: "http://mock-a:8080", TimeoutMS: 800, RateLimitRPS: 40},
			{Name: "b", BaseURL: "http://mock-b:8080", TimeoutMS: 800, RateLimitRPS: 40},
		},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/fanout", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d want 200", rec.Code)
	}
	if !stub.hadDeadline {
		t.Errorf("handler must derive a context deadline from deadline_ms")
	}
	if stub.gotRequestID != "req_abc" {
		t.Errorf("request_id = %q want req_abc", stub.gotRequestID)
	}
	if stub.gotProviders != 2 {
		t.Errorf("providers = %d want 2", stub.gotProviders)
	}
	var resp FanOutResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.RequestID != "req_abc" || len(resp.Results) != 2 {
		t.Errorf("response = %+v", resp)
	}
}

func TestFanOutHandler_RejectsBadDeadline(t *testing.T) {
	srv := newTestServer(&stubFanOuter{}, stubPinger{})
	body := `{"request_id":"x","deadline_ms":0,"query":{},"providers":[]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/fanout", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d want 400", rec.Code)
	}
}

func TestFanOutHandler_RejectsNonPost(t *testing.T) {
	srv := newTestServer(&stubFanOuter{}, stubPinger{})
	req := httptest.NewRequest(http.MethodGet, "/v1/fanout", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d want 405", rec.Code)
	}
}

func TestHealthz_OKAndUnavailable(t *testing.T) {
	srvOK := newTestServer(&stubFanOuter{}, stubPinger{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srvOK.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthy code = %d want 200", rec.Code)
	}

	srvDown := newTestServer(&stubFanOuter{}, stubPinger{err: errors.New("redis down")})
	rec2 := httptest.NewRecorder()
	srvDown.Handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy code = %d want 503", rec2.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	srv := newTestServer(&stubFanOuter{}, stubPinger{})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics code = %d want 200", rec.Code)
	}
}
