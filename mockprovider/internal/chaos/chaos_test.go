package chaos

import (
	"math"
	"testing"
	"time"
)

// withinPP asserts observed is within tolerance (percentage points, as a
// fraction) of want.
func withinPP(t *testing.T, label string, observed, want, tolPP float64) {
	t.Helper()
	if math.Abs(observed-want) > tolPP {
		t.Errorf("%s: observed %.3f, want %.3f ±%.3f", label, observed, want, tolPP)
	}
}

func TestFlakyDistribution(t *testing.T) {
	const n = 100000 // large sample -> tight observed distribution
	e := NewEngine(ProfileFlaky, 42, 80*time.Millisecond)

	var n500, nTrunc, nReset, nSlow, nNormal int
	for i := 0; i < n; i++ {
		d := e.Decide()
		switch d.Action {
		case ActionError500:
			n500++
		case ActionTruncated:
			nTrunc++
		case ActionReset:
			nReset++
		case ActionNormal:
			if d.Latency > 5*80*time.Millisecond {
				nSlow++ // the ×10 latency bucket
			} else {
				nNormal++
			}
		}
	}
	f := func(x int) float64 { return float64(x) / float64(n) }

	// ±5pp is the spec tolerance; with 100k samples we're comfortably inside.
	withinPP(t, "500", f(n500), 0.15, 0.05)
	withinPP(t, "truncated", f(nTrunc), 0.10, 0.05)
	withinPP(t, "slow(x10)", f(nSlow), 0.10, 0.05)
	withinPP(t, "reset", f(nReset), 0.05, 0.05)
	withinPP(t, "normal", f(nNormal), 0.60, 0.05)
}

func TestStableMostlyNormal(t *testing.T) {
	const n = 100000
	e := NewEngine(ProfileStable, 7, 80*time.Millisecond)
	var n500 int
	for i := 0; i < n; i++ {
		if e.Decide().Action == ActionError500 {
			n500++
		}
	}
	withinPP(t, "stable 500", float64(n500)/float64(n), 0.01, 0.05)
}

func TestSlowProfile(t *testing.T) {
	const n = 100000
	e := NewEngine(ProfileSlow, 99, 80*time.Millisecond)
	var nOver10s int
	for i := 0; i < n; i++ {
		d := e.Decide()
		if d.Action != ActionNormal {
			t.Fatalf("slow profile must always be normal action, got %v", d.Action)
		}
		if d.Latency > 10*time.Second {
			nOver10s++
		}
	}
	withinPP(t, "slow >10s", float64(nOver10s)/float64(n), 0.05, 0.05)
}

func TestDownAlwaysRefuses(t *testing.T) {
	e := NewEngine(ProfileDown, 1, 80*time.Millisecond)
	for i := 0; i < 1000; i++ {
		if e.Decide().Action != ActionConnRefused {
			t.Fatal("down profile must always refuse")
		}
	}
}

func TestSetProfile(t *testing.T) {
	e := NewEngine(ProfileStable, 1, 80*time.Millisecond)
	if err := e.SetProfile("down"); err != nil {
		t.Fatal(err)
	}
	if e.Profile() != ProfileDown {
		t.Fatalf("profile not switched, got %s", e.Profile())
	}
	if err := e.SetProfile("nonsense"); err == nil {
		t.Fatal("expected error for unknown profile")
	}
}

func TestDeterministicChaosSequence(t *testing.T) {
	// Same seed -> same decision sequence (reproducible chaos for tests).
	e1 := NewEngine(ProfileFlaky, 12345, 80*time.Millisecond)
	e2 := NewEngine(ProfileFlaky, 12345, 80*time.Millisecond)
	for i := 0; i < 500; i++ {
		if e1.Decide().Action != e2.Decide().Action {
			t.Fatalf("decision %d differs between identically-seeded engines", i)
		}
	}
}
