// Package c implements the Adapter for Provider C (SPEC §5.5): an XML-ish
// abomination encoded in JSON — string-typed numbers, "DD.MM.YYYY HH:mm" LOCAL
// times interpreted in the ORIGIN airport's timezone, RSD currency.
//
//	{ "Flights": { "Flight": [ { "Id": "C-55", "Number": "JU351",
//	  "Org": "BEG", "Dst": "AMS",
//	  "Departure": "01.07.2026 08:40", "Arrival": "01.07.2026 11:05",
//	  "Fare": { "Total": "16690.00", "Currency": "RSD" } } ] } }
//
// Local times use the origin airport's tz (SPEC §5.5/§5.4) so the canonical
// depart_at/arrive_at carry the correct UTC offset.
package c

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
)

const providerID = "c"

type wire struct {
	Flights struct {
		Flight []flight `json:"Flight"`
	} `json:"Flights"`
}

type flight struct {
	ID        string `json:"Id"`
	Number    string `json:"Number"`
	Org       string `json:"Org"`
	Dst       string `json:"Dst"`
	Departure string `json:"Departure"`
	Arrival   string `json:"Arrival"`
	Fare      struct {
		Total    string `json:"Total"`
		Currency string `json:"Currency"`
	} `json:"Fare"`
}

// Adapter is the Provider C adapter.
type Adapter struct {
	cfg    adapter.ProviderConfig
	client *http.Client
	fx     normalize.FX
}

// New constructs a Provider C adapter (Registry constructor).
func New(cfg adapter.ProviderConfig) adapter.Adapter {
	return &Adapter{cfg: cfg, client: &http.Client{}, fx: normalize.DefaultFX()}
}

// NewWithDeps injects an FX table and HTTP client for tests.
func NewWithDeps(cfg adapter.ProviderConfig, client *http.Client, fx normalize.FX) adapter.Adapter {
	return &Adapter{cfg: cfg, client: client, fx: fx}
}

// Name returns the provider id.
func (c *Adapter) Name() string { return providerID }

// Search performs one attempt against Provider C.
func (c *Adapter) Search(ctx context.Context, q model.Query) ([]model.Offer, error) {
	body, _ := json.Marshal(q)
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/search"
	raw, err := normalize.DoSearch(ctx, c.client, url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	return c.parse(raw)
}

func (c *Adapter) parse(raw []byte) ([]model.Offer, error) {
	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("%w: provider c json: %v", adapter.ErrBadPayload, err)
	}

	offers := make([]model.Offer, 0, len(w.Flights.Flight))
	for _, f := range w.Flights.Flight {
		// Local times are in the ORIGIN airport timezone.
		loc, ok := normalize.Location(f.Org)
		if !ok {
			return nil, fmt.Errorf("%w: provider c unknown origin tz %q", adapter.ErrBadPayload, f.Org)
		}
		dep, err := normalize.ParseLocalCETime(f.Departure, loc)
		if err != nil {
			return nil, fmt.Errorf("%w: provider c departure %q: %v", adapter.ErrBadPayload, f.Departure, err)
		}
		arr, err := parseArrival(f.Arrival, loc)
		if err != nil {
			return nil, fmt.Errorf("%w: provider c arrival %q: %v", adapter.ErrBadPayload, f.Arrival, err)
		}
		amount, err := normalize.ParseDecimalString(f.Fare.Total)
		if err != nil {
			return nil, fmt.Errorf("%w: provider c fare %q: %v", adapter.ErrBadPayload, f.Fare.Total, err)
		}
		priceEUR, err := c.fx.ToEUR(amount, f.Fare.Currency)
		if err != nil {
			return nil, fmt.Errorf("%w: provider c fx: %v", adapter.ErrBadPayload, err)
		}
		carrier := f.Number
		if len(carrier) >= 2 {
			carrier = carrier[:2]
		}
		seg := model.Segment{
			FlightNo:    f.Number,
			Carrier:     carrier,
			Origin:      f.Org,
			Destination: f.Dst,
			DepartAt:    dep,
			ArriveAt:    arr,
		}
		offer, err := normalize.Assemble(providerID, f.ID, priceEUR, []model.Segment{seg})
		if err != nil {
			return nil, fmt.Errorf("%w: provider c assemble: %v", adapter.ErrBadPayload, err)
		}
		offers = append(offers, offer)
	}
	return offers, nil
}

// parseArrival parses the arrival local time. If the parsed arrival is before
// departure on the same wall clock (e.g. an overnight flight rolling past
// midnight is represented with an earlier HH:mm), the caller's segment ordering
// still holds because both times are absolute; we simply parse in the origin
// tz as the spec prescribes. (Provider C does not encode the destination tz, so
// the origin tz is the documented, honest interpretation.)
func parseArrival(s string, loc *time.Location) (time.Time, error) {
	return normalize.ParseLocalCETime(s, loc)
}
