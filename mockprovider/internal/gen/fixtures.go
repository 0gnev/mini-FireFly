package gen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Airport mirrors a row in fixtures/airports.json.
type Airport struct {
	IATA string `json:"iata"`
	City string `json:"city"`
	TZ   string `json:"tz"`
}

// routesFile mirrors fixtures/routes.json.
type routesFile struct {
	Bidirectional bool       `json:"bidirectional"`
	Routes        [][]string `json:"routes"`
}

// Fixtures holds the frozen contract data (airports + allowed routes) loaded at
// startup. It is read-only after Load.
type Fixtures struct {
	airports map[string]Airport // keyed by IATA (uppercased)
	routes   map[string]bool    // keyed by "ORIG|DEST", expanded for bidirectionality
}

var (
	loadOnce sync.Once
	loaded   *Fixtures
	loadErr  error
)

// DefaultDir returns the fixtures directory. It honours FIXTURES_DIR when set,
// otherwise falls back to /fixtures (where the Docker image bakes them).
func DefaultDir() string {
	if d := os.Getenv("FIXTURES_DIR"); d != "" {
		return d
	}
	return "/fixtures"
}

// Load reads airports.json and routes.json from dir. It is cached: the first
// successful call wins for the process lifetime.
func Load(dir string) (*Fixtures, error) {
	loadOnce.Do(func() {
		loaded, loadErr = loadFrom(dir)
	})
	return loaded, loadErr
}

func loadFrom(dir string) (*Fixtures, error) {
	f := &Fixtures{
		airports: make(map[string]Airport),
		routes:   make(map[string]bool),
	}

	airportsRaw, err := os.ReadFile(filepath.Join(dir, "airports.json"))
	if err != nil {
		return nil, fmt.Errorf("read airports.json: %w", err)
	}
	var airports []Airport
	if err := json.Unmarshal(airportsRaw, &airports); err != nil {
		return nil, fmt.Errorf("parse airports.json: %w", err)
	}
	for _, a := range airports {
		f.airports[strings.ToUpper(a.IATA)] = a
	}

	routesRaw, err := os.ReadFile(filepath.Join(dir, "routes.json"))
	if err != nil {
		return nil, fmt.Errorf("read routes.json: %w", err)
	}
	var rf routesFile
	if err := json.Unmarshal(routesRaw, &rf); err != nil {
		return nil, fmt.Errorf("parse routes.json: %w", err)
	}
	for _, pair := range rf.Routes {
		if len(pair) != 2 {
			continue
		}
		o, d := strings.ToUpper(pair[0]), strings.ToUpper(pair[1])
		f.routes[routeKey(o, d)] = true
		if rf.Bidirectional {
			f.routes[routeKey(d, o)] = true
		}
	}

	if len(f.airports) == 0 {
		return nil, fmt.Errorf("no airports loaded from %s", dir)
	}
	if len(f.routes) == 0 {
		return nil, fmt.Errorf("no routes loaded from %s", dir)
	}
	return f, nil
}

func routeKey(origin, dest string) string {
	return origin + "|" + dest
}

// Airport returns the airport record for an IATA code and whether it exists.
func (f *Fixtures) Airport(iata string) (Airport, bool) {
	a, ok := f.airports[strings.ToUpper(iata)]
	return a, ok
}

// RouteAllowed reports whether origin->destination is a permitted route.
func (f *Fixtures) RouteAllowed(origin, dest string) bool {
	return f.routes[routeKey(strings.ToUpper(origin), strings.ToUpper(dest))]
}
