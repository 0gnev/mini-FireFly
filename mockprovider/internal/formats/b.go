package formats

import (
	"encoding/json"
	"math"
	"time"

	"github.com/mini-firefly/mockprovider/internal/gen"
)

// Provider B — nested, unix-epoch seconds (UTC), price in integer cents, USD,
// segments mandatory.
//
//	{ "data": { "itineraries": [ { "token": "b81xx",
//	  "pricing": { "amount_cents": 15890, "ccy": "USD" },
//	  "segments": [ { "no": "KL1342", "carrier": "KL", "o": "BEG", "d": "AMS",
//	  "dep_ts": 1751352000, "arr_ts": 1751360700 } ] } ] } }
type bSegment struct {
	No      string `json:"no"`
	Carrier string `json:"carrier"`
	O       string `json:"o"`
	D       string `json:"d"`
	DepTS   int64  `json:"dep_ts"`
	ArrTS   int64  `json:"arr_ts"`
}

type bPricing struct {
	AmountCents int64  `json:"amount_cents"`
	CCY         string `json:"ccy"`
}

type bItinerary struct {
	Token    string     `json:"token"`
	Pricing  bPricing   `json:"pricing"`
	Segments []bSegment `json:"segments"`
}

type bEnvelope struct {
	Data struct {
		Itineraries []bItinerary `json:"itineraries"`
	} `json:"data"`
}

func renderB(offers []gen.Offer) ([]byte, string, error) {
	var env bEnvelope
	env.Data.Itineraries = make([]bItinerary, 0, len(offers))
	for _, o := range offers {
		usd := o.PriceEUR * eurToUSD
		cents := int64(math.Round(usd * 100))

		segs := make([]bSegment, 0, len(o.Segments))
		for _, s := range o.Segments {
			segs = append(segs, bSegment{
				No:      s.FlightNo,
				Carrier: s.Carrier,
				O:       s.Origin,
				D:       s.Destination,
				DepTS:   epochUTC(s.DepartLocal, s.OriginLoc),
				ArrTS:   epochUTC(s.ArriveLocal, s.DestLoc),
			})
		}
		env.Data.Itineraries = append(env.Data.Itineraries, bItinerary{
			Token:    "b" + o.NativeID,
			Pricing:  bPricing{AmountCents: cents, CCY: "USD"},
			Segments: segs,
		})
	}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, "", err
	}
	return b, "application/json", nil
}

// epochUTC converts a naive wall-clock time in loc to a UTC unix timestamp.
func epochUTC(wall time.Time, loc *time.Location) int64 {
	t := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), 0, loc)
	return t.UTC().Unix()
}
