package wall

import (
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Probe is what a driver found on this host.
type Probe struct {
	// CanPrepare reports verified boot inputs; it is not compatibility readiness or admission.
	CanPrepare bool `json:"can_prepare,omitempty"`
	Driver  string            `json:"driver"`
	Level   Level             `json:"level"`
	OK      bool              `json:"ok"`
	Reason  string            `json:"reason,omitempty"`
	Fix     string            `json:"fix,omitempty"`
	Version string            `json:"version,omitempty"`
	Facts   map[string]string `json:"facts,omitempty"`
}

// Best is the strongest valid level whose driver probed OK.
func Best(probes []Probe) Level {
	best := None
	for _, p := range probes {
		if p.OK && p.Level > best && p.Level <= MicroVM {
			best = p.Level
		}
	}
	return best
}

var registry struct {
	sync.RWMutex
	drivers []Driver
}

// Register adds a complete driver built into this helper. A diagnostic probe
// for a runtime that is not implemented must not register a driver.
func Register(d Driver) error {
	if d == nil {
		return fmt.Errorf("register wall driver: driver is nil")
	}
	name := d.Name()
	if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, " \t\r\n") {
		return fmt.Errorf("register wall driver: invalid name %q", name)
	}
	if d.Level() < None || d.Level() > MicroVM {
		return fmt.Errorf("register wall driver %s: invalid level %d", name, d.Level())
	}
	registry.Lock()
	defer registry.Unlock()
	for _, old := range registry.drivers {
		if old.Name() == name {
			return fmt.Errorf("register wall driver %s: name already registered", name)
		}
	}
	registry.drivers = append(registry.drivers, d)
	return nil
}

// Drivers lists the complete drivers built into this helper, strongest first.
// No sandbox runtime is registered until its Prepare and Open paths exist.
func Drivers() []Driver {
	registry.RLock()
	drivers := slices.Clone(registry.drivers)
	registry.RUnlock()
	slices.SortFunc(drivers, func(a, b Driver) int {
		if a.Level() != b.Level() {
			return int(b.Level()) - int(a.Level())
		}
		// Nitro runs a first-level guest where another VMM would run nested.
		if a.Name() == "nitro" && b.Name() != "nitro" {
			return -1
		}
		if b.Name() == "nitro" && a.Name() != "nitro" {
			return 1
		}
		return strings.Compare(a.Name(), b.Name())
	})
	return drivers
}
