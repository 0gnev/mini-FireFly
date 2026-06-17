package c

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
	cfg := adapter.ProviderConfig{Name: "c", BaseURL: baseURL, TimeoutMS: 500}
	return NewWithDeps(cfg, &http.Client{}, normalize.DefaultFX())
}

const validBody = `{"Flights":{"Flight":[
  {"Id":"C-55","Number":"JU351","Org":"BEG","Dst":"AMS",
   "Departure":"01.07.2026 08:40","Arrival":"01.07.2026 11:05",
   "Fare":{"Total":"16690.00","Currency":"RSD"}}
]}}`

func TestProviderC_RSD_LocalTimes_NormalizeToEUR(t *testing.T) {
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
	// 16690.00 RSD × 0.0085 = 141.865 -> "141.87".
	if o.Price != "141.87" {
		t.Errorf("price = %q want 141.87", o.Price)
	}
	if o.Currency != "EUR" {
		t.Errorf("currency = %q want EUR", o.Currency)
	}
	// BEG local 08:40 on 2026-07-01 (summer) is +02:00 -> equals 06:40Z.
	wantDep := time.Date(2026, 7, 1, 6, 40, 0, 0, time.UTC)
	if !o.DepartAt.Equal(wantDep) {
		t.Errorf("depart_at = %v want %v (06:40Z)", o.DepartAt, wantDep)
	}
	// Verify the offset carried is +02:00.
	_, offset := o.DepartAt.Zone()
	if offset != 2*3600 {
		t.Errorf("depart_at offset = %ds want 7200s (+02:00)", offset)
	}
	if o.DurationMinutes != 145 {
		t.Errorf("duration = %d want 145", o.DurationMinutes)
	}
}

// A flight whose origin is in a different timezone must use THAT origin's tz.
func TestProviderC_OriginTimezone_Dubai(t *testing.T) {
	// DXB is Asia/Dubai (+04:00, no DST).
	body := `{"Flights":{"Flight":[
	  {"Id":"C-99","Number":"EK072","Org":"DXB","Dst":"FRA",
	   "Departure":"01.07.2026 09:00","Arrival":"01.07.2026 14:00",
	   "Fare":{"Total":"100000.00","Currency":"RSD"}}
	]}}`
	srv := fakeServer(t, 200, body)
	ad := newAdapter(srv.URL)
	offers, err := ad.Search(context.Background(), model.Query{Origin: "DXB", Destination: "FRA"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, offset := offers[0].DepartAt.Zone()
	if offset != 4*3600 {
		t.Errorf("DXB offset = %ds want 14400s (+04:00)", offset)
	}
	// 09:00 +04:00 == 05:00Z.
	if !offers[0].DepartAt.Equal(time.Date(2026, 7, 1, 5, 0, 0, 0, time.UTC)) {
		t.Errorf("depart_at = %v want 05:00Z", offers[0].DepartAt.UTC())
	}
}

func TestProviderC_Truncated_BadPayload(t *testing.T) {
	srv := fakeServer(t, 200, validBody[:len(validBody)-15])
	ad := newAdapter(srv.URL)
	_, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS"})
	if !errors.Is(err, adapter.ErrBadPayload) {
		t.Fatalf("want ErrBadPayload, got %v", err)
	}
}

func TestProviderC_Empty(t *testing.T) {
	srv := fakeServer(t, 200, `{"Flights":{"Flight":[]}}`)
	ad := newAdapter(srv.URL)
	offers, err := ad.Search(context.Background(), model.Query{Origin: "BEG", Destination: "AMS"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(offers) != 0 {
		t.Fatalf("want 0 offers, got %d", len(offers))
	}
}
