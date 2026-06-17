// Package model holds the FROZEN canonical types shared across the fanout
// service (SPEC §6.1) plus the per-provider status enum (SPEC §4.3).
//
// These types are a frozen contract: their JSON tags and field shapes must not
// change. core (Laravel) and fanout agree on this wire format.
package model

import "time"

// Query is the canonical search query (SPEC §4.1 / §6.1).
type Query struct {
	Origin      string  `json:"origin"`
	Destination string  `json:"destination"`
	DepartDate  string  `json:"depart_date"` // YYYY-MM-DD
	ReturnDate  *string `json:"return_date,omitempty"`
	Passengers  int     `json:"passengers"`
}

// Segment is one flight leg of an offer (SPEC §6.1).
type Segment struct {
	FlightNo    string    `json:"flight_no"`
	Carrier     string    `json:"carrier"`
	Origin      string    `json:"origin"`
	Destination string    `json:"destination"`
	DepartAt    time.Time `json:"depart_at"`
	ArriveAt    time.Time `json:"arrive_at"`
}

// Offer is the canonical normalized offer (SPEC §4.2 / §6.1). After
// normalization, Price is a decimal string in EUR and Currency is always "EUR".
type Offer struct {
	OfferID         string    `json:"offer_id"`
	Provider        string    `json:"provider"`
	Price           string    `json:"price"`    // decimal string, EUR after normalization
	Currency        string    `json:"currency"` // always "EUR" post-normalization
	DepartAt        time.Time `json:"depart_at"`
	ArriveAt        time.Time `json:"arrive_at"`
	DurationMinutes int       `json:"duration_minutes"`
	Stops           int       `json:"stops"`
	Segments        []Segment `json:"segments"`
}

// ProviderStatus is the per-provider outcome enum (SPEC §4.3).
type ProviderStatus string

// Status enum values (SPEC §4.3). skipped_disabled is part of the canonical
// enum but is owned by core (fanout never receives disabled providers), so it
// is defined here for completeness but never emitted by fanout.
const (
	StatusOK              ProviderStatus = "ok"
	StatusTimeout         ProviderStatus = "timeout"
	StatusError           ProviderStatus = "error"
	StatusRateLimited     ProviderStatus = "rate_limited"
	StatusBreakerOpen     ProviderStatus = "breaker_open"
	StatusBadPayload      ProviderStatus = "bad_payload"
	StatusSkippedDisabled ProviderStatus = "skipped_disabled"
)

// ProviderResult is one provider's outcome within a fan-out (SPEC §6.1).
type ProviderResult struct {
	Provider  string         `json:"provider"`
	Status    ProviderStatus `json:"status"`
	Offers    []Offer        `json:"offers,omitempty"`
	LatencyMS int64          `json:"latency_ms"`
	Attempts  int            `json:"attempts"`
	Error     string         `json:"error,omitempty"` // sanitized, no internal addrs
}
