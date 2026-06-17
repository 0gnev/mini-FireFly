package d

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/mini-firefly/fanout/internal/model"
	"github.com/mini-firefly/fanout/internal/providers/normalize"
)

func fakeServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newAdapter(baseURL string) adapter.Adapter {
	cfg := adapter.ProviderConfig{Name: "d", BaseURL: baseURL, TimeoutMS: 500}
	return NewWithDeps(cfg, &http.Client{}, normalize.DefaultFX())
}

const validBody = `{"oid":"d-17","fn":"OS772","org":"BEG","dst":"AMS","dep":"2026-07-01T07:10:00+02:00","arr":"2026-07-01T09:20:00+02:00","p":"139.90","cur":"EUR"}
{"oid":"d-18","fn":"LH998","org":"BEG","dst":"AMS","dep":"2026-07-01T12:00:00+02:00","arr":"2026-07-01T14:15:00+02:00","p":"180.00","cur":"EUR"}
`

func TestProviderD_NDJSON_Valid(t *testing.T) {
	srv := fakeServer(t, 200, validBody)
	ad := newAdapter(srv.URL)

	offers, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS", DepartDate: "2026-07-01", Passengers: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(offers) != 2 {
		t.Fatalf("want 2 offers, got %d", len(offers))
	}
	o := offers[0]
	if o.Price != "139.90" {
		t.Errorf("price = %q want 139.90", o.Price)
	}
	if o.Currency != "EUR" {
		t.Errorf("currency = %q want EUR", o.Currency)
	}
	// 07:10 -> 09:20 = 130 minutes.
	if o.DurationMinutes != 130 {
		t.Errorf("duration = %d want 130", o.DurationMinutes)
	}
	if o.OfferID != normalize.OfferID("d", "d-17") {
		t.Errorf("offer_id = %q", o.OfferID)
	}
	if o.Segments[0].Carrier != "OS" {
		t.Errorf("carrier = %q want OS", o.Segments[0].Carrier)
	}
}

func TestProviderD_Empty(t *testing.T) {
	srv := fakeServer(t, 200, "")
	ad := newAdapter(srv.URL)
	offers, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("want 0 offers, got %d", len(offers))
	}
}

func TestProviderD_TruncatedLine_BadPayload(t *testing.T) {
	// Second line is cut mid-JSON.
	truncated := validBody[:len(validBody)-40]
	srv := fakeServer(t, 200, truncated)
	ad := newAdapter(srv.URL)
	_, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS"})
	if !errors.Is(err, adapter.ErrBadPayload) {
		t.Fatalf("want ErrBadPayload, got %v", err)
	}
}

// Wrong-currency line still normalizes via the FX table (USD here).
func TestProviderD_USDCurrency_NormalizeToEUR(t *testing.T) {
	body := `{"oid":"d-9","fn":"AB100","org":"BEG","dst":"AMS","dep":"2026-07-01T07:10:00+02:00","arr":"2026-07-01T09:20:00+02:00","p":"100.00","cur":"USD"}` + "\n"
	srv := fakeServer(t, 200, body)
	ad := newAdapter(srv.URL)
	offers, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 100.00 USD × 0.92 = 92.00.
	if offers[0].Price != "92.00" {
		t.Errorf("price = %q want 92.00", offers[0].Price)
	}
}
