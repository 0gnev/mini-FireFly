package formats

import (
	"bufio"
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mini-firefly/mockprovider/internal/gen"
)

// sampleOffer builds a single deterministic offer for BEG->AMS with known
// times, used to assert each format's currency/time handling.
func sampleOffer(t *testing.T) gen.Offer {
	t.Helper()
	beg, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	// Wall-clock components (zone stripped to UTC per gen contract).
	dep := time.Date(2026, 7, 1, 8, 40, 0, 0, time.UTC)
	// arrival in DEST (Amsterdam) wall-clock: 11:05 local.
	arr := time.Date(2026, 7, 1, 11, 5, 0, 0, time.UTC)
	return gen.Offer{
		NativeID: "x-1",
		PriceEUR: 100.00,
		Segments: []gen.Segment{{
			FlightNo:    "JU351",
			Carrier:     "JU",
			Origin:      "BEG",
			Destination: "AMS",
			DepartLocal: dep,
			ArriveLocal: arr,
			OriginLoc:   beg,
			DestLoc:     ams,
		}},
	}
}

func TestFormatA(t *testing.T) {
	o := sampleOffer(t)
	body, ct, err := renderA([]gen.Offer{o})
	if err != nil {
		t.Fatal(err)
	}
	if ct != "application/json" {
		t.Fatalf("content-type %q", ct)
	}
	var env aEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("A body not valid json: %v", err)
	}
	if len(env.Results) != 1 {
		t.Fatalf("want 1 result, got %d", len(env.Results))
	}
	r := env.Results[0]
	if r.PriceEUR != 100.00 {
		t.Errorf("A price_eur = %v, want 100", r.PriceEUR)
	}
	// Belgrade is +02:00 in July (CEST).
	if !strings.HasSuffix(r.Dep, "+02:00") {
		t.Errorf("A dep should carry +02:00 offset: %s", r.Dep)
	}
}

func TestFormatB_USDCentsAndEpoch(t *testing.T) {
	o := sampleOffer(t)
	body, _, err := renderB([]gen.Offer{o})
	if err != nil {
		t.Fatal(err)
	}
	var env bEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("B body not valid json: %v", err)
	}
	it := env.Data.Itineraries[0]
	if it.Pricing.CCY != "USD" {
		t.Errorf("B ccy = %s", it.Pricing.CCY)
	}
	// 100 EUR -> USD cents; fanout converts back USD->EUR=0.92.
	gotEUR := float64(it.Pricing.AmountCents) / 100.0 * 0.92
	if math.Abs(gotEUR-100.0) > 0.02 {
		t.Errorf("B round-trip EUR = %.4f, want ~100", gotEUR)
	}
	// Epoch is UTC: Belgrade 08:40 +02:00 == 06:40 UTC.
	depUTC := time.Unix(it.Segments[0].DepTS, 0).UTC()
	if depUTC.Hour() != 6 || depUTC.Minute() != 40 {
		t.Errorf("B dep epoch UTC = %02d:%02d, want 06:40", depUTC.Hour(), depUTC.Minute())
	}
}

func TestFormatC_RSDStringsAndOriginTZ(t *testing.T) {
	o := sampleOffer(t)
	body, _, err := renderC([]gen.Offer{o})
	if err != nil {
		t.Fatal(err)
	}
	var env cEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("C body not valid json: %v", err)
	}
	f := env.Flights.Flight[0]
	if f.Fare.Currency != "RSD" {
		t.Errorf("C currency = %s", f.Fare.Currency)
	}
	// Total is a STRING; round-trips via RSD->EUR=0.0085.
	total, err := strconv.ParseFloat(f.Fare.Total, 64)
	if err != nil {
		t.Fatalf("C total not numeric string: %v", err)
	}
	gotEUR := total * 0.0085
	if math.Abs(gotEUR-100.0) > 0.05 {
		t.Errorf("C round-trip EUR = %.4f, want ~100", gotEUR)
	}
	// Departure string in origin (Belgrade) tz: DD.MM.YYYY HH:mm.
	if f.Departure != "01.07.2026 08:40" {
		t.Errorf("C departure = %q, want 01.07.2026 08:40", f.Departure)
	}
	// Arrival is expressed in ORIGIN tz. AMS 11:05 +02:00 == BEG 11:05 +02:00
	// (same offset in July), so it should read 11:05.
	if f.Arrival != "01.07.2026 11:05" {
		t.Errorf("C arrival = %q, want 01.07.2026 11:05 (origin tz)", f.Arrival)
	}
	// Parsing the C arrival string with the ORIGIN tz must recover the correct
	// instant (this is exactly what the fanout C adapter does).
	beg, _ := time.LoadLocation("Europe/Belgrade")
	parsed, err := time.ParseInLocation(cTimeLayout, f.Arrival, beg)
	if err != nil {
		t.Fatal(err)
	}
	wantInstant := time.Date(2026, 7, 1, 11, 5, 0, 0, mustLoad(t, "Europe/Amsterdam"))
	if !parsed.Equal(wantInstant) {
		t.Errorf("C arrival instant mismatch: got %s want %s", parsed.UTC(), wantInstant.UTC())
	}
}

func TestFormatD_NDJSON(t *testing.T) {
	offers := []gen.Offer{sampleOffer(t), sampleOffer(t)}
	offers[1].NativeID = "x-2"
	body, ct, err := renderD(offers)
	if err != nil {
		t.Fatal(err)
	}
	if ct != "application/x-ndjson" {
		t.Fatalf("D content-type = %s", ct)
	}
	// Exactly one JSON object per line, each independently valid.
	sc := bufio.NewScanner(bytes.NewReader(body))
	lines := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var l dLine
		if err := json.Unmarshal(line, &l); err != nil {
			t.Fatalf("D line not valid json: %v (%s)", err, line)
		}
		if l.Cur != "EUR" {
			t.Errorf("D cur = %s", l.Cur)
		}
		if l.P != "100.00" {
			t.Errorf("D price string = %q, want 100.00", l.P)
		}
		lines++
	}
	if lines != 2 {
		t.Fatalf("D expected 2 NDJSON lines, got %d", lines)
	}
}

func TestFormatsAreDistinct(t *testing.T) {
	o := []gen.Offer{sampleOffer(t)}
	a, _, _ := renderA(o)
	b, _, _ := renderB(o)
	c, _, _ := renderC(o)
	d, _, _ := renderD(o)
	bodies := map[string][]byte{"a": a, "b": b, "c": c, "d": d}
	for n1, b1 := range bodies {
		for n2, b2 := range bodies {
			if n1 < n2 && bytes.Equal(b1, b2) {
				t.Errorf("formats %s and %s are byte-identical", n1, n2)
			}
		}
	}
}

func TestRenderUnknownProvider(t *testing.T) {
	if _, _, err := Render("z", nil); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
