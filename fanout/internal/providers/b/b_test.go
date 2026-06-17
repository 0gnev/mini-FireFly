package b

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

func fakeServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newAdapter(baseURL string) adapter.Adapter {
	cfg := adapter.ProviderConfig{Name: "b", BaseURL: baseURL, TimeoutMS: 500}
	return NewWithDeps(cfg, &http.Client{}, normalize.DefaultFX())
}

// dep_ts 1782888000 = 2026-07-01T06:40:00Z; arr_ts 1782896700 = 2026-07-01T09:05:00Z.
const validBody = `{"data":{"itineraries":[
  {"token":"b81xx","pricing":{"amount_cents":15890,"ccy":"USD"},
   "segments":[{"no":"KL1342","carrier":"KL","o":"BEG","d":"AMS","dep_ts":1782888000,"arr_ts":1782896700}]}
]}}`

func TestProviderB_USDCents_NormalizeToEUR(t *testing.T) {
	srv := fakeServer(t, 200, validBody)
	ad := newAdapter(srv.URL)

	offers, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS", DepartDate: "2026-07-01", Passengers: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("want 1 offer, got %d", len(offers))
	}
	o := offers[0]
	// 15890 cents = 158.90 USD; × 0.92 = 146.188 -> "146.19".
	if o.Price != "146.19" {
		t.Errorf("price = %q want 146.19", o.Price)
	}
	if o.Currency != "EUR" {
		t.Errorf("currency = %q want EUR", o.Currency)
	}
	// Epoch is UTC: dep at 2026-07-01T06:40:00Z.
	wantDep := time.Date(2026, 7, 1, 6, 40, 0, 0, time.UTC)
	if !o.DepartAt.Equal(wantDep) {
		t.Errorf("depart_at = %v want %v", o.DepartAt.UTC(), wantDep)
	}
	if o.DepartAt.Location() != time.UTC {
		t.Errorf("depart_at should be UTC, got loc %v", o.DepartAt.Location())
	}
	// 06:40Z -> 09:05Z = 145 minutes.
	if o.DurationMinutes != 145 {
		t.Errorf("duration = %d want 145", o.DurationMinutes)
	}
	if o.OfferID != normalize.OfferID("b", "b81xx") {
		t.Errorf("offer_id = %q", o.OfferID)
	}
}

func TestProviderB_Empty(t *testing.T) {
	srv := fakeServer(t, 200, `{"data":{"itineraries":[]}}`)
	ad := newAdapter(srv.URL)
	offers, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("want 0 offers, got %d", len(offers))
	}
}

func TestProviderB_Truncated_BadPayload(t *testing.T) {
	srv := fakeServer(t, 200, validBody[:len(validBody)-20])
	ad := newAdapter(srv.URL)
	_, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS"})
	if !errors.Is(err, adapter.ErrBadPayload) {
		t.Fatalf("want ErrBadPayload, got %v", err)
	}
}

func TestProviderB_NoSegments_BadPayload(t *testing.T) {
	body := `{"data":{"itineraries":[{"token":"x","pricing":{"amount_cents":100,"ccy":"USD"},"segments":[]}]}}`
	srv := fakeServer(t, 200, body)
	ad := newAdapter(srv.URL)
	_, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS"})
	if !errors.Is(err, adapter.ErrBadPayload) {
		t.Fatalf("want ErrBadPayload for missing segments, got %v", err)
	}
}
