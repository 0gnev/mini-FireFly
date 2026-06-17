package chaos

import (
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// Profile is a chaos profile name.
type Profile string

const (
	ProfileStable Profile = "stable"
	ProfileFlaky  Profile = "flaky"
	ProfileSlow   Profile = "slow"
	ProfileDown   Profile = "down"
)

// ValidProfile reports whether s names a known profile.
func ValidProfile(s string) bool {
	switch Profile(strings.ToLower(s)) {
	case ProfileStable, ProfileFlaky, ProfileSlow, ProfileDown:
		return true
	}
	return false
}

// Action is the decided behavior for a single /search request.
type Action int

const (
	// ActionNormal: serve a valid response after Latency.
	ActionNormal Action = iota
	// ActionError500: respond HTTP 500 after Latency.
	ActionError500
	// ActionTruncated: send a JSON body cut at a random byte > 10, status 200,
	// Content-Type still JSON.
	ActionTruncated
	// ActionReset: close the socket without any response (connection reset).
	ActionReset
	// ActionConnRefused: down-profile — hijack and close immediately (no read).
	ActionConnRefused
)

func (a Action) String() string {
	switch a {
	case ActionNormal:
		return "normal"
	case ActionError500:
		return "error_500"
	case ActionTruncated:
		return "truncated"
	case ActionReset:
		return "reset"
	case ActionConnRefused:
		return "conn_refused"
	default:
		return "unknown"
	}
}

// Decision is the outcome of evaluating chaos for one request.
type Decision struct {
	Action  Action
	Latency time.Duration
	// TruncateAt, when Action==ActionTruncated, is the byte index (>10) at which
	// to cut the response body. The actual cut is min(TruncateAt, len(body)).
	TruncateAt int
}

// Engine evaluates chaos decisions deterministically from a seeded PRNG. All
// randomness flows through one mutex-guarded source so a fixed CHAOS_SEED yields
// a reproducible sequence of decisions (SPEC §5.3 / §14.1 statistical tests).
type Engine struct {
	mu          sync.Mutex
	rng         *rand.Rand
	profile     Profile
	baseLatency time.Duration
}

// NewEngine builds an Engine with the given starting profile, seed and base
// latency.
func NewEngine(profile Profile, seed int64, baseLatency time.Duration) *Engine {
	if !ValidProfile(string(profile)) {
		profile = ProfileStable
	}
	return &Engine{
		rng:         rand.New(rand.NewSource(seed)),
		profile:     Profile(strings.ToLower(string(profile))),
		baseLatency: baseLatency,
	}
}

// Profile returns the current profile.
func (e *Engine) Profile() Profile {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.profile
}

// SetProfile switches the active profile at runtime. Returns an error if the
// profile is unknown.
func (e *Engine) SetProfile(p string) error {
	if !ValidProfile(p) {
		return fmt.Errorf("unknown profile %q", p)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.profile = Profile(strings.ToLower(p))
	return nil
}

// Decide evaluates the active profile's probabilities once, in spec order,
// first-match-wins, and returns the action plus latency to apply.
func (e *Engine) Decide() Decision {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch e.profile {
	case ProfileDown:
		return Decision{Action: ActionConnRefused}

	case ProfileStable:
		// 1% HTTP 500; otherwise valid. Latency = base ±30% uniform.
		if e.rng.Float64() < 0.01 {
			return Decision{Action: ActionError500, Latency: e.jitter(e.baseLatency, 0.30)}
		}
		return Decision{Action: ActionNormal, Latency: e.jitter(e.baseLatency, 0.30)}

	case ProfileFlaky:
		// Evaluated in order, first match wins:
		//   15% HTTP 500
		//   10% truncated JSON body (status 200)
		//   10% latency ×10
		//    5% connection reset
		//   rest normal (base ±30%)
		r := e.rng.Float64()
		switch {
		case r < 0.15:
			return Decision{Action: ActionError500, Latency: e.jitter(e.baseLatency, 0.30)}
		case r < 0.25:
			at := 11 + e.rng.Intn(40) // > 10
			return Decision{Action: ActionTruncated, Latency: e.jitter(e.baseLatency, 0.30), TruncateAt: at}
		case r < 0.35:
			return Decision{Action: ActionNormal, Latency: e.jitter(e.baseLatency*10, 0.30)}
		case r < 0.40:
			return Decision{Action: ActionReset}
		default:
			return Decision{Action: ActionNormal, Latency: e.jitter(e.baseLatency, 0.30)}
		}

	case ProfileSlow:
		// latency = 5×base ±50%; 5% chance latency > 10s.
		if e.rng.Float64() < 0.05 {
			// 10s..15s, guaranteed to bust a sane deadline.
			extra := time.Duration(e.rng.Int63n(int64(5 * time.Second)))
			return Decision{Action: ActionNormal, Latency: 10*time.Second + extra}
		}
		return Decision{Action: ActionNormal, Latency: e.jitter(5*e.baseLatency, 0.50)}

	default:
		return Decision{Action: ActionNormal, Latency: e.jitter(e.baseLatency, 0.30)}
	}
}

// jitter returns d scaled by a uniform factor in [1-frac, 1+frac].
func (e *Engine) jitter(d time.Duration, frac float64) time.Duration {
	if d <= 0 {
		return 0
	}
	factor := 1 + (e.rng.Float64()*2-1)*frac
	return time.Duration(float64(d) * factor)
}
