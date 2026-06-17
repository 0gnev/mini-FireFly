// Package d implements the Adapter for Provider D (SPEC §5.5): newline-
// delimited JSON, one offer per line (streaming-style), ISO times, price as a
// decimal string, EUR.
//
//	{"oid":"d-17","fn":"OS772","org":"BEG","dst":"AMS",
//	 "dep":"2026-07-01T07:10:00+02:00","arr":"2026-07-01T09:20:00+02:00",
//	 "p":"139.90","cur":"EUR"}
//	{"oid":"d-18", ...}
//
// NDJSON: each non-empty line is one offer. A line that fails to parse is
// treated as a truncated/corrupt body -> bad_payload.
package d

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/mini-firefly/fanout/internal/model"
	"github.com/mini-firefly/fanout/internal/providers/normalize"
)

const providerID = "d"

type line struct {
	OID string `json:"oid"`
	FN  string `json:"fn"`
	Org string `json:"org"`
	Dst string `json:"dst"`
	Dep string `json:"dep"`
	Arr string `json:"arr"`
	P   string `json:"p"`
	Cur string `json:"cur"`
}

// Adapter is the Provider D adapter.
type Adapter struct {
	cfg    adapter.ProviderConfig
	client *http.Client
	fx     normalize.FX
}

// New constructs a Provider D adapter (Registry constructor).
func New(cfg adapter.ProviderConfig) adapter.Adapter {
	return &Adapter{cfg: cfg, client: &http.Client{}, fx: normalize.DefaultFX()}
}

// NewWithDeps injects an FX table and HTTP client for tests.
func NewWithDeps(cfg adapter.ProviderConfig, client *http.Client, fx normalize.FX) adapter.Adapter {
	return &Adapter{cfg: cfg, client: client, fx: fx}
}

// Name returns the provider id.
func (d *Adapter) Name() string { return providerID }

// Search performs one attempt against Provider D.
func (d *Adapter) Search(ctx context.Context, q model.Query) ([]model.Offer, error) {
	body, _ := json.Marshal(q)
	url := strings.TrimRight(d.cfg.BaseURL, "/") + "/search"
	raw, err := normalize.DoSearch(ctx, d.client, url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	return d.parse(raw)
}

func (d *Adapter) parse(raw []byte) ([]model.Offer, error) {
	offers := make([]model.Offer, 0)
	sc := bufio.NewScanner(bytes.NewReader(raw))
	// Allow long lines (default token is 64 KiB).
	sc.Buffer(make([]byte, 0, 64*1024), normalize.MaxBodyBytes)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var ln line
		if err := json.Unmarshal([]byte(text), &ln); err != nil {
			// A corrupt/truncated NDJSON line is a malformed body.
			return nil, fmt.Errorf("%w: provider d line %d: %v", adapter.ErrBadPayload, lineNo, err)
		}
		dep, err := time.Parse(time.RFC3339, ln.Dep)
		if err != nil {
			return nil, fmt.Errorf("%w: provider d dep %q: %v", adapter.ErrBadPayload, ln.Dep, err)
		}
		arr, err := time.Parse(time.RFC3339, ln.Arr)
		if err != nil {
			return nil, fmt.Errorf("%w: provider d arr %q: %v", adapter.ErrBadPayload, ln.Arr, err)
		}
		amount, err := normalize.ParseDecimalString(ln.P)
		if err != nil {
			return nil, fmt.Errorf("%w: provider d price %q: %v", adapter.ErrBadPayload, ln.P, err)
		}
		priceEUR, err := d.fx.ToEUR(amount, ln.Cur)
		if err != nil {
			return nil, fmt.Errorf("%w: provider d fx: %v", adapter.ErrBadPayload, err)
		}
		carrier := ln.FN
		if len(carrier) >= 2 {
			carrier = carrier[:2]
		}
		seg := model.Segment{
			FlightNo:    ln.FN,
			Carrier:     carrier,
			Origin:      ln.Org,
			Destination: ln.Dst,
			DepartAt:    dep,
			ArriveAt:    arr,
		}
		offer, err := normalize.Assemble(providerID, ln.OID, priceEUR, []model.Segment{seg})
		if err != nil {
			return nil, fmt.Errorf("%w: provider d assemble: %v", adapter.ErrBadPayload, err)
		}
		offers = append(offers, offer)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: provider d scan: %v", adapter.ErrBadPayload, err)
	}
	return offers, nil
}
