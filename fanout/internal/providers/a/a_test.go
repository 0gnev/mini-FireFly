package a

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/mini-firefly/fanout/internal/model"
	"github.com/mini-firefly/fanout/internal/providers/normalize"
)

func fakeServer(t *testing.T, status int, contentType, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newAdapter(baseURL string) adapter.Adapter {
	cfg := adapter.ProviderConfig{Name: "a", BaseURL: baseURL, TimeoutMS: 500}
	return NewWithDeps(cfg, &http.Client{}, normalize.DefaultFX())
}

const validBody = `{"results":[
  {"id":"A-981","flight":"JU351","from":"BEG","to":"AMS","dep":"2026-07-01T08:40:00+02:00","arr":"2026-07-01T11:05:00+02:00","price_eur":142.5,"legs":[]},
  {"id":"A-982","flight":"KL3001","from":"BEG","to":"AMS","dep":"2026-07-01T13:00:00+02:00","arr":"2026-07-01T15:35:00+02:00","price_eur":205.0,"legs":[]}
]}`

func TestProviderA_Valid(t *testing.T) {
	srv := fakeServer(t, 200, "application/json", validBody)
	ad := newAdapter(srv.URL)

	offers, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS", DepartDate: "2026-07-01", Passengers: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(offers) != 2 {
		t.Fatalf("want 2 offers, got %d", len(offers))
	}
	o := offers[0]
	if o.Provider != "a" {
		t.Errorf("provider = %q want a", o.Provider)
	}
	if o.OfferID != normalize.OfferID("a", "A-981") {
		t.Errorf("offer_id = %q want %q", o.OfferID, normalize.OfferID("a", "A-981"))
	}
	if o.Currency != "EUR" {
		t.Errorf("currency = %q want EUR", o.Currency)
	}
	if o.Price != "142.50" {
		t.Errorf("price = %q want 142.50", o.Price)
	}
	if o.Stops != 0 {
		t.Errorf("stops = %d want 0", o.Stops)
	}
	// 08:40 -> 11:05 = 145 minutes.
	if o.DurationMinutes != 145 {
		t.Errorf("duration = %d want 145", o.DurationMinutes)
	}
	if !o.DepartAt.Equal(mustTime(t, "2026-07-01T08:40:00+02:00")) {
		t.Errorf("depart_at = %v", o.DepartAt)
	}
	if len(o.Segments) != 1 || o.Segments[0].Carrier != "JU" {
		t.Errorf("segment carrier wrong: %+v", o.Segments)
	}
}

func TestProviderA_Truncated_BadPayload(t *testing.T) {
	truncated := validBody[:len(validBody)-25] // cut the JSON mid-structure
	srv := fakeServer(t, 200, "application/json", truncated)
	ad := newAdapter(srv.URL)

	_, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS", DepartDate: "2026-07-01", Passengers: 1})
	if !errors.Is(err, adapter.ErrBadPayload) {
		t.Fatalf("want ErrBadPayload, got %v", err)
	}
}

func TestProviderA_Empty(t *testing.T) {
	srv := fakeServer(t, 200, "application/json", `{"results":[]}`)
	ad := newAdapter(srv.URL)

	offers, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS", DepartDate: "2026-07-01", Passengers: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("want 0 offers, got %d", len(offers))
	}
}

func TestProviderA_429_RateLimited(t *testing.T) {
	srv := fakeServer(t, http.StatusTooManyRequests, "application/json", `{"error":"rate limited"}`)
	ad := newAdapter(srv.URL)
	_, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS"})
	if !errors.Is(err, adapter.ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
}

func TestProviderA_500_Upstream(t *testing.T) {
	srv := fakeServer(t, http.StatusInternalServerError, "application/json", `boom`)
	ad := newAdapter(srv.URL)
	_, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS"})
	if !errors.Is(err, adapter.ErrUpstream) {
		t.Fatalf("want ErrUpstream, got %v", err)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad time %q: %v", s, err)
	}
	return ts
}
