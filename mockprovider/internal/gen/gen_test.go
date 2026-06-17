package gen

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fixturesDir locates the repo's fixtures directory relative to this test file
// (…/mockprovider/internal/gen → repo root /fixtures), honouring FIXTURES_DIR.
func fixturesDir(t *testing.T) string {
	t.Helper()
	if d := os.Getenv("FIXTURES_DIR"); d != "" {
		return d
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller")
	}
	// file = .../mockprovider/internal/gen/gen_test.go
	dir := filepath.Dir(file)
	root := filepath.Clean(filepath.Join(dir, "..", "..", ".."))
	return filepath.Join(root, "fixtures")
}

func loadFixtures(t *testing.T) *Fixtures {
	t.Helper()
	fx, err := loadFrom(fixturesDir(t)) // bypass the once-cache for test isolation
	if err != nil {
		t.Fatalf("load fixtures: %v", err)
	}
	return fx
}

func TestDeterminism_SameQuerySameOffers(t *testing.T) {
	fx := loadFixtures(t)
	q := Query{Origin: "BEG", Destination: "AMS", DepartDate: "2026-07-01", Passengers: 1}

	first, err := Generate("a", q, fx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := Generate("a", q, fx)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d differs from first run; generation is not deterministic", i)
		}
	}
	if n := len(first); n < 3 || n > 8 {
		t.Fatalf("offer count %d outside 3..8", n)
	}
}

func TestDeterminism_SeedStable(t *testing.T) {
	// Seed must depend only on the four named inputs.
	a := Seed("a", "BEG", "AMS", "2026-07-01")
	b := Seed("a", "BEG", "AMS", "2026-07-01")
	if a != b {
		t.Fatalf("seed not stable: %d != %d", a, b)
	}
	c := Seed("b", "BEG", "AMS", "2026-07-01")
	if a == c {
		t.Fatal("seed should differ by provider id")
	}
	d := Seed("a", "BEG", "CDG", "2026-07-01")
	if a == d {
		t.Fatal("seed should differ by destination")
	}
}

func TestProviderFlavors_DifferByProvider(t *testing.T) {
	fx := loadFixtures(t)
	q := Query{Origin: "BEG", Destination: "AMS", DepartDate: "2026-07-01", Passengers: 1}
	a, _ := Generate("a", q, fx)
	b, _ := Generate("b", q, fx)
	if reflect.DeepEqual(a, b) {
		t.Fatal("provider a and b produced identical offers; flavors not distinct")
	}
}

func TestUnknownRoute_EmptyList(t *testing.T) {
	fx := loadFixtures(t)
	// JFK<->ATH is not in routes.json.
	q := Query{Origin: "JFK", Destination: "ATH", DepartDate: "2026-07-01", Passengers: 1}
	offers, err := Generate("a", q, fx)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 0 {
		t.Fatalf("expected empty offer list for unknown route, got %d", len(offers))
	}
}

func TestKnownRoute_Bidirectional(t *testing.T) {
	fx := loadFixtures(t)
	// routes.json has [AMS,JFK]; reverse must also be allowed.
	if !fx.RouteAllowed("JFK", "AMS") {
		t.Fatal("reverse route JFK->AMS should be allowed (bidirectional)")
	}
	q := Query{Origin: "JFK", Destination: "AMS", DepartDate: "2026-07-01", Passengers: 1}
	offers, err := Generate("a", q, fx)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) == 0 {
		t.Fatal("expected offers for known reverse route")
	}
}

func TestPriceMultiplierApplied(t *testing.T) {
	if Multiplier("a") != 1.00 || Multiplier("b") != 0.95 || Multiplier("c") != 1.10 || Multiplier("d") != 1.02 {
		t.Fatal("price multipliers do not match spec")
	}
}

// dedupKey mirrors core's §4.4 duplicate criterion: identical
// (carrier, flight_no, depart instant, arrive instant) across the segment list.
func dedupKey(o Offer) string {
	var b strings.Builder
	for _, s := range o.Segments {
		dep := time.Date(s.DepartLocal.Year(), s.DepartLocal.Month(), s.DepartLocal.Day(),
			s.DepartLocal.Hour(), s.DepartLocal.Minute(), 0, 0, s.OriginLoc).UTC()
		arr := time.Date(s.ArriveLocal.Year(), s.ArriveLocal.Month(), s.ArriveLocal.Day(),
			s.ArriveLocal.Hour(), s.ArriveLocal.Minute(), 0, 0, s.DestLoc).UTC()
		fmt.Fprintf(&b, "%s|%s|%d|%d;", s.Carrier, s.FlightNo, dep.Unix(), arr.Unix())
	}
	return b.String()
}

// TestCrossProviderOverlap is the core of the I1 dedup prerequisite (SPEC §4.4,
// §5.4): for a given route+date a SMALL deterministic subset of flights is
// SHARED across providers — byte-identical (carrier, flight_no, times) but
// differing ONLY in price — so dedup and cheapest-wins actually fire.
func TestCrossProviderOverlap(t *testing.T) {
	fx := loadFixtures(t)
	q := Query{Origin: "BEG", Destination: "AMS", DepartDate: "2026-07-01", Passengers: 1}

	byProvider := map[string][]Offer{}
	for _, p := range []string{"a", "b", "c", "d"} {
		offers, err := Generate(p, q, fx)
		if err != nil {
			t.Fatalf("generate %s: %v", p, err)
		}
		byProvider[p] = offers
	}

	// Index every offer's dedup key per provider, with its price.
	type priced struct {
		provider string
		price    float64
	}
	keys := map[string][]priced{}
	for p, offers := range byProvider {
		for _, o := range offers {
			k := dedupKey(o)
			keys[k] = append(keys[k], priced{provider: p, price: o.PriceEUR})
		}
	}

	// At least one dedup key must be shared by >= 2 distinct providers.
	shared := 0
	for k, ps := range keys {
		seen := map[string]bool{}
		for _, x := range ps {
			seen[x.provider] = true
		}
		if len(seen) >= 2 {
			shared++
			// Prices must differ across providers for the shared flight so that
			// "cheapest wins" is meaningful.
			prices := map[float64]bool{}
			for _, x := range ps {
				prices[x.price] = true
			}
			if len(prices) < 2 {
				t.Errorf("shared flight %q has identical prices across providers; cheapest-wins is moot", k)
			}
		}
	}
	if shared == 0 {
		t.Fatal("no flight is shared across >=2 providers; cross-provider dedup will never fire")
	}
}

// TestOfferCountWithinSpecBand asserts the TOTAL offer count (shared + private)
// stays inside SPEC §5.4's 3..8 band for every provider.
func TestOfferCountWithinSpecBand(t *testing.T) {
	fx := loadFixtures(t)
	q := Query{Origin: "BEG", Destination: "AMS", DepartDate: "2026-07-01", Passengers: 1}
	for _, p := range []string{"a", "b", "c", "d"} {
		offers, err := Generate(p, q, fx)
		if err != nil {
			t.Fatalf("generate %s: %v", p, err)
		}
		if n := len(offers); n < 3 || n > 8 {
			t.Fatalf("provider %s offer count %d outside 3..8", p, n)
		}
	}
}
