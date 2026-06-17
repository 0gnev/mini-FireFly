// Package formats renders generated offers into per-provider wire formats.
//
// Each provider (a/b/c/d) has a GENUINELY different format (SPEC §5.5). The
// formats are defined independently here and intentionally share no code path
// with the fanout adapters, so normalization stays honest. Currency conversion
// from the canonical EUR base price happens inside each format using a fixed
// inverse of fanout's static FX table:
//
//	EUR->USD = 1/0.92   (B)
//	EUR->RSD = 1/0.0085 (C)
//
// so that fanout's USD->EUR=0.92 / RSD->EUR=0.0085 round-trips back to the base.
package formats

import (
	"fmt"
	"strings"

	"github.com/mini-firefly/mockprovider/internal/gen"
)

// Static FX inverses (see package doc). fanout uses USD->EUR=0.92, RSD->EUR=0.0085.
const (
	eurToUSD = 1.0 / 0.92
	eurToRSD = 1.0 / 0.0085
)

// SearchRequest is the canonical search input accepted by /search across all
// providers. Even though the RESPONSE format differs per provider, the request
// body is the canonical Query (the fanout adapters send canonical queries).
type SearchRequest struct {
	Origin      string  `json:"origin"`
	Destination string  `json:"destination"`
	DepartDate  string  `json:"depart_date"`
	ReturnDate  *string `json:"return_date,omitempty"`
	Passengers  int     `json:"passengers"`
}

// Render serializes offers for the given provider id. Returns the body bytes and
// the Content-Type to set. An unknown provider id is an error.
func Render(providerID string, offers []gen.Offer) ([]byte, string, error) {
	switch strings.ToLower(providerID) {
	case "a":
		return renderA(offers)
	case "b":
		return renderB(offers)
	case "c":
		return renderC(offers)
	case "d":
		return renderD(offers)
	default:
		return nil, "", fmt.Errorf("unknown provider id %q", providerID)
	}
}
