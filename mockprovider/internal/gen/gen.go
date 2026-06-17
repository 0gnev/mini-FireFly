package gen

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"strings"
	"time"
)

// Query is the normalized input used for generation. Only the four fields named
// in SPEC §5.4 feed the seed; passengers affects nothing about the offers
// (price is per-offer/per-seat-agnostic here) but is carried for completeness.
type Query struct {
	Origin      string
	Destination string
	DepartDate  string // YYYY-MM-DD
	Passengers  int
}

// Segment is one flight leg in provider-local wall-clock terms. DepartLocal /
// ArriveLocal are naive wall-clock times in the segment origin airport's
// timezone; the format packages decide how to render them (ISO offset, unix
// epoch UTC, local string, etc.).
type Segment struct {
	FlightNo    string
	Carrier     string
	Origin      string
	Destination string
	// Wall-clock local times (no zone attached) at the respective airports.
	DepartLocal time.Time
	ArriveLocal time.Time
	// Resolved IANA locations for origin/destination, for zone-aware rendering.
	OriginLoc *time.Location
	DestLoc   *time.Location
}

// Offer is the canonical, format-agnostic offer the generator emits. Each
// per-provider format package renders this into its own wire shape.
type Offer struct {
	NativeID string // provider's native offer id, e.g. "a-3"
	// Price is the EUR base price as a float, BEFORE the per-provider currency
	// conversion / multiplier handled by the format layer.
	PriceEUR float64
	Segments []Segment
}

// carrierPools are disjoint-ish per provider so cross-provider dedup and
// "cheapest wins" actually exercise (SPEC §5.4).
var carrierPools = map[string][]string{
	"a": {"JU", "LH", "AF", "BA"},
	"b": {"KL", "OS", "LX", "SN"},
	"c": {"JU", "TK", "A3", "OU"}, // JU overlaps A on purpose (dedup target)
	"d": {"OS", "FR", "W6", "EW"}, // OS overlaps B on purpose
}

// commonCarriers is the pool used to mint the SHARED flights that overlap across
// providers (SPEC §4.4 "mocks intentionally overlap routes", §5.4 "so cross-
// provider dedup and 'cheapest wins' actually exercise"). These flights are
// derived from a provider-independent seed so every provider emits the
// byte-identical (carrier, flight_no, depart_at, arrive_at, segments) tuple,
// differing ONLY in price (each provider keeps its own multiplier). Dedup in
// core (§4.4) then collapses them and "cheapest wins" picks one provider.
var commonCarriers = []string{"JU", "LH", "KL", "OS"}

// priceMultiplier per provider (SPEC §5.4).
var priceMultiplier = map[string]float64{
	"a": 1.00,
	"b": 0.95,
	"c": 1.10,
	"d": 1.02,
}

// Multiplier returns the per-provider price multiplier (default 1.0).
func Multiplier(providerID string) float64 {
	if m, ok := priceMultiplier[strings.ToLower(providerID)]; ok {
		return m
	}
	return 1.0
}

// Seed computes the deterministic FNV-64a seed from the provider id and query.
func Seed(providerID, origin, destination, departDate string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToLower(providerID)))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(strings.ToUpper(origin)))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(strings.ToUpper(destination)))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(departDate))
	return int64(h.Sum64())
}

// CommonSeed computes a provider-INDEPENDENT FNV-64a seed from the route + date
// only. It drives generation of the shared/overlapping flights so every provider
// emits the identical (carrier, flight_no, depart_at, arrive_at, segments) tuple
// for those flights, enabling cross-provider dedup (§4.4).
func CommonSeed(origin, destination, departDate string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.ToUpper(origin)))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(strings.ToUpper(destination)))
	_, _ = h.Write([]byte("|"))
	_, _ = h.Write([]byte(departDate))
	return int64(h.Sum64())
}

// Generate produces a deterministic set of 3-8 offers for the given query. The
// same (providerID, query, fixtures) always yields byte-identical offers.
// Returns an empty slice for unknown routes (a real provider behavior).
func Generate(providerID string, q Query, fx *Fixtures) ([]Offer, error) {
	origin := strings.ToUpper(q.Origin)
	dest := strings.ToUpper(q.Destination)

	if !fx.RouteAllowed(origin, dest) {
		return []Offer{}, nil
	}

	originAp, ok := fx.Airport(origin)
	if !ok {
		return []Offer{}, nil
	}
	destAp, ok := fx.Airport(dest)
	if !ok {
		return []Offer{}, nil
	}

	originLoc, err := time.LoadLocation(originAp.TZ)
	if err != nil {
		return nil, fmt.Errorf("load tz %q: %w", originAp.TZ, err)
	}
	destLoc, err := time.LoadLocation(destAp.TZ)
	if err != nil {
		return nil, fmt.Errorf("load tz %q: %w", destAp.TZ, err)
	}

	departDay, err := time.ParseInLocation("2006-01-02", q.DepartDate, time.UTC)
	if err != nil {
		return nil, fmt.Errorf("parse depart_date %q: %w", q.DepartDate, err)
	}
	year, month, day := departDay.Date()

	pid := strings.ToLower(providerID)
	mult := Multiplier(providerID)

	tg := timeGrid{year: year, month: month, day: day, originLoc: originLoc, destLoc: destLoc}

	// --- Shared (overlapping) flights, provider-INDEPENDENT carrier/flight/times.
	// Derived from CommonSeed so every provider emits the byte-identical tuple
	// for these flights (price aside). 1-2 shared flights per route+date keeps
	// the overlap "small and deterministic" per the spec while guaranteeing
	// cross-provider dedup fires (§4.4). Provider applies only its multiplier.
	commonRNG := rand.New(rand.NewSource(CommonSeed(origin, dest, q.DepartDate)))
	nShared := 1 + commonRNG.Intn(2) // 1..2 inclusive

	offers := make([]Offer, 0, nShared+8)
	for i := 0; i < nShared; i++ {
		carrier := commonCarriers[commonRNG.Intn(len(commonCarriers))]
		flightNum := 100 + commonRNG.Intn(900)
		flightNo := fmt.Sprintf("%s%d", carrier, flightNum)
		seg := tg.segment(carrier, flightNo, origin, dest, commonRNG)

		// Shared base EUR price, then THIS provider's multiplier so cheapest-wins
		// is meaningful: identical flight, different price across providers.
		baseCents := 4900 + commonRNG.Intn(40001) // 4900..44900 cents
		base := float64(baseCents) / 100.0

		offers = append(offers, Offer{
			// Native id is provider-scoped (so offer_id hashes differ) but the
			// dedup key (carrier, flight_no, depart_at, arrive_at) is identical.
			NativeID: fmt.Sprintf("%s-shared-%d", pid, i+1),
			PriceEUR: round2(base * mult),
			Segments: []Segment{seg},
		})
	}

	// --- Private flights: original per-provider deterministic generation. The
	// TOTAL offer count is kept within the spec's 3..8 band (§5.4): we draw a
	// total in [3,8] and fill the remainder after the shared flights with
	// provider-private offers (always at least 1 private flight, so a provider's
	// own carrier pool still shows).
	seed := Seed(providerID, origin, dest, q.DepartDate)
	rng := rand.New(rand.NewSource(seed))

	pool := carrierPools[pid]
	if len(pool) == 0 {
		pool = carrierPools["a"]
	}

	total := 3 + rng.Intn(6) // 3..8 inclusive total offers
	n := total - nShared
	if n < 1 {
		n = 1 // guarantee at least one private flight
	}
	for i := 0; i < n; i++ {
		carrier := pool[rng.Intn(len(pool))]
		// Flight number: carrier + 3-4 digit number, deterministic.
		flightNum := 100 + rng.Intn(900)
		flightNo := fmt.Sprintf("%s%d", carrier, flightNum)
		seg := tg.segment(carrier, flightNo, origin, dest, rng)

		// Base EUR price 49.00..449.00 in 0.10 steps, then per-provider multiplier.
		baseCents := 4900 + rng.Intn(40001) // 4900..44900 cents
		base := float64(baseCents) / 100.0
		priceEUR := round2(base * mult)

		offers = append(offers, Offer{
			NativeID: fmt.Sprintf("%s-%d", pid, i+1),
			PriceEUR: priceEUR,
			Segments: []Segment{seg},
		})
	}
	return offers, nil
}

// timeGrid carries the per-query date and resolved timezones used to build
// segments with genuine timezone math.
type timeGrid struct {
	year      int
	month     time.Month
	day       int
	originLoc *time.Location
	destLoc   *time.Location
}

// segment builds one canonical segment, drawing departure time and duration from
// rng. The same rng draw order yields identical segments, which is what makes the
// shared-flight overlap byte-stable across providers.
func (g timeGrid) segment(carrier, flightNo, origin, dest string, rng *rand.Rand) Segment {
	// Departure local wall-clock: between 06:00 and 21:00.
	depHour := 6 + rng.Intn(16)
	depMin := rng.Intn(12) * 5 // 0,5,...,55
	departLocal := time.Date(g.year, g.month, g.day, depHour, depMin, 0, 0, time.UTC)

	// Duration 70..360 minutes; arrival in DEST local time, computed from the
	// real UTC instant so timezone math is genuinely exercised downstream.
	durMin := 70 + rng.Intn(291)
	departInstant := time.Date(g.year, g.month, g.day, depHour, depMin, 0, 0, g.originLoc)
	arriveInstant := departInstant.Add(time.Duration(durMin) * time.Minute)
	arriveLocal := wallClockIn(arriveInstant, g.destLoc)

	return Segment{
		FlightNo:    flightNo,
		Carrier:     carrier,
		Origin:      origin,
		Destination: dest,
		DepartLocal: departLocal,
		ArriveLocal: arriveLocal,
		OriginLoc:   g.originLoc,
		DestLoc:     g.destLoc,
	}
}

// wallClockIn returns the wall-clock time of instant t as observed in loc,
// represented with UTC zone stripping (so callers can re-attach a zone or
// format the naive components without surprise).
func wallClockIn(t time.Time, loc *time.Location) time.Time {
	lt := t.In(loc)
	y, m, d := lt.Date()
	return time.Date(y, m, d, lt.Hour(), lt.Minute(), lt.Second(), 0, time.UTC)
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// HashNativeID returns the first 8 hex chars of sha256(nativeID). Useful for
// tests / parity with the canonical offer_id rule, but the mock emits the raw
// native id; normalization (in fanout) applies the hash.
func HashNativeID(nativeID string) string {
	sum := sha256.Sum256([]byte(nativeID))
	return hex.EncodeToString(sum[:])[:8]
}
