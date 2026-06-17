package formats

import (
	"encoding/json"
	"time"

	"github.com/mini-firefly/mockprovider/internal/gen"
)

// Provider A — flat JSON, ISO-8601 times with offset, price as float, EUR.
//
//	{ "results": [ { "id": "A-981", "flight": "JU351", "from": "BEG", "to": "AMS",
//	  "dep": "2026-07-01T08:40:00+02:00", "arr": "2026-07-01T11:05:00+02:00",
//	  "price_eur": 142.5, "legs": [] } ] }
type aResult struct {
	ID       string        `json:"id"`
	Flight   string        `json:"flight"`
	From     string        `json:"from"`
	To       string        `json:"to"`
	Dep      string        `json:"dep"`
	Arr      string        `json:"arr"`
	PriceEUR float64       `json:"price_eur"`
	Legs     []interface{} `json:"legs"`
}

type aEnvelope struct {
	Results []aResult `json:"results"`
}

func renderA(offers []gen.Offer) ([]byte, string, error) {
	env := aEnvelope{Results: make([]aResult, 0, len(offers))}
	for _, o := range offers {
		seg := o.Segments[0]
		env.Results = append(env.Results, aResult{
			ID:       upperNative(o.NativeID),
			Flight:   seg.FlightNo,
			From:     seg.Origin,
			To:       seg.Destination,
			Dep:      isoWithOffset(seg.DepartLocal, seg.OriginLoc),
			Arr:      isoWithOffset(seg.ArriveLocal, seg.DestLoc),
			PriceEUR: o.PriceEUR,
			Legs:     []interface{}{},
		})
	}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, "", err
	}
	return b, "application/json", nil
}

// isoWithOffset renders a naive wall-clock time as RFC3339 with the offset of
// loc on that date (real timezone math, DST-aware).
func isoWithOffset(wall time.Time, loc *time.Location) string {
	t := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, loc)
	return t.Format(time.RFC3339)
}

func upperNative(id string) string {
	// "a-3" -> "A-3"
	if len(id) == 0 {
		return id
	}
	return string(id[0]-'a'+'A') + id[1:]
}
