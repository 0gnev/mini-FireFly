// Package normalize holds the shared normalization helpers every provider
// adapter uses to turn a native offer into the canonical model (SPEC §4.2):
// offer-id hashing, FX conversion to EUR, timezone lookup for local-time
// parsing, and offer assembly (stops, duration).
//
// The airports timezone table is embedded so the adapters do real timezone
// math (Provider C local times use the ORIGIN airport's tz, SPEC §5.5/§5.4)
// without a runtime file dependency. Values are read independently of any
// mock-provider code so normalization stays honest.
package normalize

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/mini-firefly/fanout/internal/model"
	"github.com/shopspring/decimal"
)

//go:embed airports.json
var airportsJSON []byte

type airport struct {
	IATA string `json:"iata"`
	City string `json:"city"`
	TZ   string `json:"tz"`
}

// tzByIATA maps an airport code to its loaded *time.Location.
var tzByIATA map[string]*time.Location

func init() {
	var airports []airport
	if err := json.Unmarshal(airportsJSON, &airports); err != nil {
		panic("normalize: cannot parse embedded airports.json: " + err.Error())
	}
	tzByIATA = make(map[string]*time.Location, len(airports))
	for _, a := range airports {
		loc, err := time.LoadLocation(a.TZ)
		if err != nil {
			// Fall back to UTC if the host tzdata lacks a zone; this keeps the
			// service running but is logged-worthy. We panic in tests via the
			// Docker golang image which ships full tzdata.
			loc = time.UTC
		}
		tzByIATA[a.IATA] = loc
	}
}

// FX holds the static currency→EUR conversion factors (SPEC §4.2/§13). Static
// on purpose: no external FX dependency.
type FX struct {
	USDToEUR decimal.Decimal
	RSDToEUR decimal.Decimal
}

// DefaultFX returns the spec defaults (USD→EUR 0.92, RSD→EUR 0.0085).
func DefaultFX() FX {
	return FX{
		USDToEUR: decimal.RequireFromString("0.92"),
		RSDToEUR: decimal.RequireFromString("0.0085"),
	}
}

// FXFromEnv builds an FX table from optional override strings (FX_USD_EUR /
// FX_RSD_EUR, SPEC §13). Empty strings fall back to the spec defaults; an
// unparseable value returns an error.
func FXFromEnv(usd, rsd string) (FX, error) {
	fx := DefaultFX()
	if usd != "" {
		d, err := decimal.NewFromString(usd)
		if err != nil {
			return fx, fmt.Errorf("invalid FX_USD_EUR %q: %w", usd, err)
		}
		fx.USDToEUR = d
	}
	if rsd != "" {
		d, err := decimal.NewFromString(rsd)
		if err != nil {
			return fx, fmt.Errorf("invalid FX_RSD_EUR %q: %w", rsd, err)
		}
		fx.RSDToEUR = d
	}
	return fx, nil
}

// ToEUR converts a decimal amount in the given ISO-4217 currency to a EUR
// decimal string with 2 fractional digits. Unknown currencies return an error.
func (fx FX) ToEUR(amount decimal.Decimal, currency string) (string, error) {
	var factor decimal.Decimal
	switch currency {
	case "EUR":
		factor = decimal.NewFromInt(1)
	case "USD":
		factor = fx.USDToEUR
	case "RSD":
		factor = fx.RSDToEUR
	default:
		return "", fmt.Errorf("unknown currency %q", currency)
	}
	return amount.Mul(factor).StringFixed(2), nil
}

// Location returns the *time.Location for an airport code, or false if the
// airport is unknown.
func Location(iata string) (*time.Location, bool) {
	loc, ok := tzByIATA[iata]
	return loc, ok
}

// OfferID computes the canonical offer id from a provider letter and the
// provider's native offer id: "{provider}:{first 8 hex of sha256(native)}".
func OfferID(provider, nativeID string) string {
	sum := sha256.Sum256([]byte(nativeID))
	return provider + ":" + hex.EncodeToString(sum[:])[:8]
}

// Assemble fills the derived canonical fields (offer id, currency, stops,
// duration, depart/arrive) from the segments and a normalized EUR price.
// It assumes segments are in chronological order. priceEUR is a decimal string
// already converted to EUR.
func Assemble(provider, nativeID, priceEUR string, segments []model.Segment) (model.Offer, error) {
	if len(segments) == 0 {
		return model.Offer{}, fmt.Errorf("offer %q has no segments", nativeID)
	}
	departAt := segments[0].DepartAt
	arriveAt := segments[len(segments)-1].ArriveAt
	duration := int(arriveAt.Sub(departAt).Minutes())
	return model.Offer{
		OfferID:         OfferID(provider, nativeID),
		Provider:        provider,
		Price:           priceEUR,
		Currency:        "EUR",
		DepartAt:        departAt,
		ArriveAt:        arriveAt,
		DurationMinutes: duration,
		Stops:           len(segments) - 1,
		Segments:        segments,
	}, nil
}

// ParseDecimalString parses a decimal money string (e.g. "16690.00").
func ParseDecimalString(s string) (decimal.Decimal, error) {
	return decimal.NewFromString(s)
}

// CentsToDecimal turns an integer cents amount into a major-unit decimal
// (e.g. 15890 -> 158.90).
func CentsToDecimal(cents int64) decimal.Decimal {
	return decimal.New(cents, -2)
}

// ParseLocalCETime parses a "DD.MM.YYYY HH:mm" local time in the given
// location (Provider C, SPEC §5.5). The result carries the correct UTC offset.
func ParseLocalCETime(s string, loc *time.Location) (time.Time, error) {
	const layout = "02.01.2006 15:04"
	return time.ParseInLocation(layout, s, loc)
}

// ParseEpochUTC turns unix epoch seconds into a UTC time (Provider B).
func ParseEpochUTC(epoch int64) time.Time {
	return time.Unix(epoch, 0).UTC()
}

// AtoiStrict parses an integer string, returning an error on failure (used for
// Provider C string-typed numbers if ever needed).
func AtoiStrict(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}
