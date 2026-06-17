// Package b implements the Adapter for Provider B (SPEC §5.5): nested JSON,
// unix-epoch-seconds times (UTC), price in integer cents, USD, segments
// mandatory.
//
//	{ "data": { "itineraries": [ { "token": "b81xx",
//	  "pricing": { "amount_cents": 15890, "ccy": "USD" },
//	  "segments": [ { "no": "KL1342", "carrier": "KL", "o": "BEG", "d": "AMS",
//	  "dep_ts": 1751352000, "arr_ts": 1751360700 } ] } ] } }
//
// Epoch is UTC (SPEC §5.5). Cents/USD normalize to a EUR decimal string.
package b

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/mini-firefly/fanout/internal/model"
	"github.com/mini-firefly/fanout/internal/providers/normalize"
)

const providerID = "b"

type wire struct {
	Data struct {
		Itineraries []itinerary `json:"itineraries"`
	} `json:"data"`
}

type itinerary struct {
	Token   string `json:"token"`
	Pricing struct {
		AmountCents int64  `json:"amount_cents"`
		CCY         string `json:"ccy"`
	} `json:"pricing"`
	Segments []segment `json:"segments"`
}

type segment struct {
	No      string `json:"no"`
	Carrier string `json:"carrier"`
	O       string `json:"o"`
	D       string `json:"d"`
	DepTS   int64  `json:"dep_ts"`
	ArrTS   int64  `json:"arr_ts"`
}

// Adapter is the Provider B adapter.
type Adapter struct {
	cfg    adapter.ProviderConfig
	client *http.Client
	fx     normalize.FX
}

// New constructs a Provider B adapter (Registry constructor).
func New(cfg adapter.ProviderConfig) adapter.Adapter {
	return &Adapter{cfg: cfg, client: &http.Client{}, fx: normalize.DefaultFX()}
}

// NewWithDeps injects an FX table and HTTP client for tests.
func NewWithDeps(cfg adapter.ProviderConfig, client *http.Client, fx normalize.FX) adapter.Adapter {
	return &Adapter{cfg: cfg, client: client, fx: fx}
}

// Name returns the provider id.
func (b *Adapter) Name() string { return providerID }

// Search performs one attempt against Provider B.
func (b *Adapter) Search(ctx context.Context, q model.Query) ([]model.Offer, error) {
	body, _ := json.Marshal(q)
	url := strings.TrimRight(b.cfg.BaseURL, "/") + "/search"
	raw, err := normalize.DoSearch(ctx, b.client, url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	return b.parse(raw)
}

func (b *Adapter) parse(raw []byte) ([]model.Offer, error) {
	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("%w: provider b json: %v", adapter.ErrBadPayload, err)
	}

	offers := make([]model.Offer, 0, len(w.Data.Itineraries))
	for _, it := range w.Data.Itineraries {
		if len(it.Segments) == 0 {
			return nil, fmt.Errorf("%w: provider b itinerary %q has no segments", adapter.ErrBadPayload, it.Token)
		}
		segs := make([]model.Segment, 0, len(it.Segments))
		for _, s := range it.Segments {
			segs = append(segs, model.Segment{
				FlightNo:    s.No,
				Carrier:     s.Carrier,
				Origin:      s.O,
				Destination: s.D,
				DepartAt:    normalize.ParseEpochUTC(s.DepTS), // epoch is UTC
				ArriveAt:    normalize.ParseEpochUTC(s.ArrTS),
			})
		}
		amount := normalize.CentsToDecimal(it.Pricing.AmountCents)
		priceEUR, err := b.fx.ToEUR(amount, it.Pricing.CCY)
		if err != nil {
			return nil, fmt.Errorf("%w: provider b fx: %v", adapter.ErrBadPayload, err)
		}
		offer, err := normalize.Assemble(providerID, it.Token, priceEUR, segs)
		if err != nil {
			return nil, fmt.Errorf("%w: provider b assemble: %v", adapter.ErrBadPayload, err)
		}
		offers = append(offers, offer)
	}
	return offers, nil
}
