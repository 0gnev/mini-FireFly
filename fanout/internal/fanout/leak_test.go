package fanout

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/mini-firefly/fanout/internal/model"
	"github.com/mini-firefly/fanout/internal/providers/normalize"
	"go.uber.org/goleak"
)

// TestMain runs goleak after the whole package's tests so any leaked goroutine
// from a fan-out (including the 100-iteration connection-refused leak test) is
// caught (SPEC §6.5).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// The redis client / http transport may keep idle reaper goroutines that
		// are not leaks; none are created here, but ignore the common stdlib
		// background loops just in case the test environment spins them.
		goleak.IgnoreTopFunction("internal/poll.runtime_pollWait"),
	)
}

// connRefuseAdapter dials a closed port on every Search, simulating a `down`
// provider (connection refused). It wraps failures in the sentinel errors via
// the shared HTTP helper so behavior matches production.
type connRefuseAdapter struct {
	name   string
	client *http.Client
	url    string
}

func (c *connRefuseAdapter) Name() string { return c.name }
func (c *connRefuseAdapter) Search(ctx context.Context, q model.Query) ([]model.Offer, error) {
	_, err := normalize.DoSearch(ctx, c.client, c.url, "application/json", nil)
	return nil, err
}

func TestFanOut_NoGoroutineLeak_AgainstDownProvider(t *testing.T) {
	// Find a port that is definitely closed (listen then immediately close).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // now connections are refused

	client := &http.Client{}
	reg := adapter.Registry{
		"down": func(_ adapter.ProviderConfig) adapter.Adapter {
			return &connRefuseAdapter{name: "down", client: client, url: "http://" + addr + "/search"}
		},
	}
	svc := newService(reg, &fakeBreaker{}, &fakeLimiter{}, &fakeBulkhead{})

	provs := cfgs("down")
	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		rs := svc.FanOut(ctx, "leak", model.Query{}, provs)
		cancel()
		if len(rs) != 1 {
			t.Fatalf("iteration %d: want 1 result, got %d", i, len(rs))
		}
		// Connection refused is mapped to upstream (retryable); either error or
		// timeout is acceptable, but never ok.
		if rs[0].Status == model.StatusOK {
			t.Fatalf("iteration %d: down provider returned ok", i)
		}
	}
	// goleak runs in TestMain after all tests; give in-flight transports a beat
	// to unwind so the final check is clean.
	client.CloseIdleConnections()
	time.Sleep(50 * time.Millisecond)
}
