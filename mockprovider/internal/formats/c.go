package formats

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/mini-firefly/mockprovider/internal/gen"
)

// Provider C — "XML-ish abomination encoded in JSON": string-typed numbers,
// `DD.MM.YYYY HH:mm` LOCAL times, RSD.
//
//	{ "Flights": { "Flight": [ { "Id": "C-55", "Number": "JU351",
//	  "Org": "BEG", "Dst": "AMS",
//	  "Departure": "01.07.2026 08:40", "Arrival": "01.07.2026 11:05",
//	  "Fare": { "Total": "16690.00", "Currency": "RSD" } } ] } }
//
// IMPORTANT (SPEC §5.5): C's local times are interpreted in the ORIGIN airport's
// timezone — BOTH Departure and Arrival. So the arrival wall-clock here is the
// arrival instant expressed in the ORIGIN timezone, not the destination's.
type cFare struct {
	Total    string `json:"Total"`
	Currency string `json:"Currency"`
}

type cFlight struct {
	ID        string `json:"Id"`
	Number    string `json:"Number"`
	Org       string `json:"Org"`
	Dst       string `json:"Dst"`
	Departure string `json:"Departure"`
	Arrival   string `json:"Arrival"`
	Fare      cFare  `json:"Fare"`
}

type cEnvelope struct {
	Flights struct {
		Flight []cFlight `json:"Flight"`
	} `json:"Flights"`
}

const cTimeLayout = "02.01.2006 15:04"

func renderC(offers []gen.Offer) ([]byte, string, error) {
	var env cEnvelope
	env.Flights.Flight = make([]cFlight, 0, len(offers))
	for _, o := range offers {
		seg := o.Segments[0]

		// Departure as wall-clock in origin tz (it already is).
		depStr := time.Date(seg.DepartLocal.Year(), seg.DepartLocal.Month(), seg.DepartLocal.Day(),
			seg.DepartLocal.Hour(), seg.DepartLocal.Minute(), 0, 0, time.UTC).Format(cTimeLayout)

		// Arrival: take the true arrival INSTANT and express it in the ORIGIN
		// timezone, so an adapter parsing with the origin tz recovers the correct
		// instant.
		arrInstant := time.Date(seg.ArriveLocal.Year(), seg.ArriveLocal.Month(), seg.ArriveLocal.Day(),
			seg.ArriveLocal.Hour(), seg.ArriveLocal.Minute(), 0, 0, seg.DestLoc)
		arrInOrigin := arrInstant.In(seg.OriginLoc)
		arrStr := arrInOrigin.Format(cTimeLayout)

		rsd := o.PriceEUR * eurToRSD
		total := fmt.Sprintf("%.2f", math.Round(rsd*100)/100)

		env.Flights.Flight = append(env.Flights.Flight, cFlight{
			ID:        upperNative(o.NativeID),
			Number:    seg.FlightNo,
			Org:       seg.Origin,
			Dst:       seg.Destination,
			Departure: depStr,
			Arrival:   arrStr,
			Fare:      cFare{Total: total, Currency: "RSD"},
		})
	}
	b, err := json.Marshal(env)
	if err != nil {
		return nil, "", err
	}
	return b, "application/json", nil
}
