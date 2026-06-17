package formats

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/mini-firefly/mockprovider/internal/gen"
)

// Provider D — newline-delimited JSON (one offer per line, streaming-style),
// price as string, EUR.
//
//	{"oid":"d-17","fn":"OS772","org":"BEG","dst":"AMS","dep":"...","arr":"...","p":"139.90","cur":"EUR"}
//	{"oid":"d-18", ...}
type dLine struct {
	OID string `json:"oid"`
	FN  string `json:"fn"`
	Org string `json:"org"`
	Dst string `json:"dst"`
	Dep string `json:"dep"`
	Arr string `json:"arr"`
	P   string `json:"p"`
	Cur string `json:"cur"`
}

func renderD(offers []gen.Offer) ([]byte, string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf) // Encoder writes a trailing newline per Encode
	enc.SetEscapeHTML(false)
	for _, o := range offers {
		seg := o.Segments[0]
		line := dLine{
			OID: o.NativeID,
			FN:  seg.FlightNo,
			Org: seg.Origin,
			Dst: seg.Destination,
			Dep: isoWithOffset(seg.DepartLocal, seg.OriginLoc),
			Arr: isoWithOffset(seg.ArriveLocal, seg.DestLoc),
			P:   fmt.Sprintf("%.2f", o.PriceEUR),
			Cur: "EUR",
		}
		if err := enc.Encode(line); err != nil {
			return nil, "", err
		}
	}
	// Content-Type for NDJSON; body is the buffer (already newline-delimited).
	return buf.Bytes(), "application/x-ndjson", nil
}
