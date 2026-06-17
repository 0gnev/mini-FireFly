// Package providers wires the per-provider adapters into a single Registry
// (SPEC §6.2). Adding a provider == add one entry here plus its adapter package.
package providers

import (
	"net/http"

	"github.com/mini-firefly/fanout/internal/adapter"
	"github.com/mini-firefly/fanout/internal/providers/a"
	"github.com/mini-firefly/fanout/internal/providers/b"
	"github.com/mini-firefly/fanout/internal/providers/c"
	"github.com/mini-firefly/fanout/internal/providers/d"
	"github.com/mini-firefly/fanout/internal/providers/normalize"
)

// depsCtor is the shape of each adapter's dependency-injecting constructor.
type depsCtor func(cfg adapter.ProviderConfig, client *http.Client, fx normalize.FX) adapter.Adapter

// NewRegistry returns the default provider registry. The FX table is injected
// into every constructor so a single env-configured FX flows to all adapters.
func NewRegistry(fx normalize.FX) adapter.Registry {
	return adapter.Registry{
		"a": withFX(a.NewWithDeps, fx),
		"b": withFX(b.NewWithDeps, fx),
		"c": withFX(c.NewWithDeps, fx),
		"d": withFX(d.NewWithDeps, fx),
	}
}

// withFX binds the FX table (and a fresh default http.Client) to an adapter
// constructor, yielding the Registry's expected func(cfg) Adapter signature.
func withFX(ctor depsCtor, fx normalize.FX) func(cfg adapter.ProviderConfig) adapter.Adapter {
	return func(cfg adapter.ProviderConfig) adapter.Adapter {
		return ctor(cfg, &http.Client{}, fx)
	}
}
