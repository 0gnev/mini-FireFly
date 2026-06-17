// Package a implements the Adapter for Provider A (SPEC §5.5): flat JSON, ISO
// RFC-3339 times with offset, price as a float in EUR.
//
//	{ "results": [ { "id": "A-981", "flight": "JU351", "from": "BEG", "to": "AMS",
//	  "dep": "2026-07-01T08:40:00+02:00", "arr": "2026-07-01T11:05:00+02:00",
//	  "price_eur": 142.5, "legs": [] } ] }
//
// The wire format is defined here independently of mockprovider code.
package a

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/mini-firefly/fanout/internal/model"
	"github.com/mini-firefly/fanout/internal/providers/normalize"
	"github.com/shopspring/decimal"
)

const providerID = "a"

type wire struct {
	Results []wireResult `json:"results"`
}

type wireResult struct {
	ID       string  `json:"id"`
	Flight   string  `json:"flight"`
	From     string  `json:"from"`
	To       string  `json:"to"`
	Dep      string  `json:"dep"`
	Arr      string  `json:"arr"`
	PriceEUR float64 `json:"price_eur"`
}

// Adapter is the Provider A adapter.
type Adapter struct {
	cfg    adapter.ProviderConfig
	client *http.Client
	fx     normalize.FX
}

// New constructs a Provider A adapter. It is the Registry constructor.
func New(cfg adapter.ProviderConfig) adapter.Adapter {
	return &Adapter{
		cfg:    cfg,
		client: &http.Client{}, // per-attempt deadline rides on ctx
		fx:     normalize.DefaultFX(),
	}
}

// NewWithDeps is used by tests to inject an FX table and HTTP client.
func NewWithDeps(cfg adapter.ProviderConfig, client *http.Client, fx normalize.FX) adapter.Adapter {
	return &Adapter{cfg: cfg, client: client, fx: fx}
}

// Name returns the provider id.
func (a *Adapter) Name() string { return providerID }

// Search performs one attempt against Provider A.
func (a *Adapter) Search(ctx context.Context, q model.Query) ([]model.Offer, error) {
	reqBody := buildRequest(q)
	url := strings.TrimRight(a.cfg.BaseURL, "/") + "/search"

	raw, err := normalize.DoSearch(ctx, a.client, url, "application/json", strings.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	return a.parse(raw)
}

func buildRequest(q model.Query) string {
	b, _ := json.Marshal(q)
	return string(b)
}

func (a *Adapter) parse(raw []byte) ([]model.Offer, error) {
	var w wire
	// Tolerate unknown fields the mock might add; only fail on structural
	// corruption such as a truncated body (the flaky-profile hostility).
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("%w: provider a json: %v", adapter.ErrBadPayload, err)
	}

	offers := make([]model.Offer, 0, len(w.Results))
	for _, r := range w.Results {
		dep, err := time.Parse(time.RFC3339, r.Dep)
		if err != nil {
			return nil, fmt.Errorf("%w: provider a dep time %q: %v", adapter.ErrBadPayload, r.Dep, err)
		}
		arr, err := time.Parse(time.RFC3339, r.Arr)
		if err != nil {
			return nil, fmt.Errorf("%w: provider a arr time %q: %v", adapter.ErrBadPayload, r.Arr, err)
		}
		price := decimal.NewFromFloat(r.PriceEUR)
		priceEUR, err := a.fx.ToEUR(price, "EUR")
		if err != nil {
			return nil, fmt.Errorf("%w: provider a fx: %v", adapter.ErrBadPayload, err)
		}
		carrier := r.Flight
		if len(carrier) >= 2 {
			carrier = carrier[:2]
		}
		seg := model.Segment{
			FlightNo:    r.Flight,
			Carrier:     carrier,
			Origin:      r.From,
			Destination: r.To,
			DepartAt:    dep,
			ArriveAt:    arr,
		}
		offer, err := normalize.Assemble(providerID, r.ID, priceEUR, []model.Segment{seg})
		if err != nil {
			return nil, fmt.Errorf("%w: provider a assemble: %v", adapter.ErrBadPayload, err)
		}
		offers = append(offers, offer)
	}
	return offers, nil
}
