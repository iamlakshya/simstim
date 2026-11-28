// Package wall builds and probes the isolation wall around a session's sandbox
// (isolation spec 5.1).
package wall

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

// Level is how strong a wall is. A higher level is stronger.
type Level int

const (
	// None is W0: no wall, as before isolation.
	None Level = 0
	// Process is W1: namespaces, Landlock and seccomp on the host kernel,
	// built with bubblewrap.
	Process Level = 1
	// GVisor is W2: gVisor's user-space kernel in front of the host kernel.
	GVisor Level = 2
	// MicroVM is W3: a guest kernel on KVM or Nitro.
	MicroVM Level = 3
)

// ParseLevel reads "none", "process", "gvisor" or "microvm".
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case api.LevelNone:
		return None, nil
	case api.LevelProcess:
		return Process, nil
	case api.LevelGVisor:
		return GVisor, nil
	case api.LevelMicroVM:
		return MicroVM, nil
	default:
		return None, fmt.Errorf("unknown wall level %q (use none, process, gvisor or microvm)", s)
	}
}

// String returns the level's name, as users type it.
func (l Level) String() string {
	switch l {
	case None:
		return api.LevelNone
	case Process:
		return api.LevelProcess
	case GVisor:
		return api.LevelGVisor
	case MicroVM:
		return api.LevelMicroVM
	default:
		return api.LevelNone
	}
}

// MarshalJSON encodes l as its level name string.
func (l Level) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(l.String())
	if err != nil {
		return nil, fmt.Errorf("encode wall level: %w", err)
	}
	return b, nil
}

// UnmarshalJSON decodes l from its level name string.
func (l *Level) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("decode wall level: %w", err)
	}
	v, err := ParseLevel(s)
	if err != nil {
		return err
	}
	*l = v
	return nil
}

// RootMode says whether a sandbox's root file system resets across starts.
type RootMode int

const (
	// RootReset resets the root file system on each start, keeping /work and
	// agent state.
	RootReset RootMode = iota
	// RootKeep keeps root file system changes across starts.
	RootKeep
)

// ImageRef names a pinned guest image for microvm sandboxes.
type ImageRef struct {
	Ref      string
	Digest   string
	RootHash string
	Path     string
}

// Mount is one granted host folder in a sandbox.
type Mount struct {
	Host  string
	Guest string
	Write bool
}

// Spec describes the sandbox to prepare.
type Spec struct {
	ID         string
	SessionDir string
	Image      *ImageRef
	Mounts     []Mount
	StateDirs  []string
	MemoryMax  int64
	CPUs       int
	DiskMax    int64
	Root       RootMode
	Helper     string
}

// Abilities are optional abilities of a sandbox.
type Abilities struct {
	Freeze  bool
	Reflink bool
}

// Exit describes how a sandbox ended.
type Exit struct {
	Code   int
	Signal string
	Err    error
}

// Sandbox is one session's sandbox.
type Sandbox interface {
	Start(ctx context.Context) (net.Conn, error)
	Stop(ctx context.Context, grace time.Duration) error
	Kill() error
	Wait() Exit
	Destroy() error
	Abilities() Abilities
}

// Driver builds walls of one level on this host.
type Driver interface {
	Name() string
	Level() Level
	Probe(ctx context.Context) Probe
	Prepare(ctx context.Context, spec Spec) (Sandbox, error)
	Open(ctx context.Context, sessionDir string) (Sandbox, error)
}

// Probe is what a driver found on this host (isolation spec 5.1).
type Probe struct {
	Driver  string            `json:"driver"`
	Level   Level             `json:"level"`
	OK      bool              `json:"ok"`
	Reason  string            `json:"reason,omitempty"`
	Fix     string            `json:"fix,omitempty"`
	Version string            `json:"version,omitempty"`
	Facts   map[string]string `json:"facts,omitempty"`
}

// ToAPI turns p into its wire representation.
func (p Probe) ToAPI() api.WallProbe {
	var facts map[string]string
	if len(p.Facts) > 0 {
		facts = make(map[string]string, len(p.Facts))
		for k, v := range p.Facts {
			facts[k] = v
		}
	}
	return api.WallProbe{
		Driver:  p.Driver,
		Level:   p.Level.String(),
		OK:      p.OK,
		Reason:  p.Reason,
		Fix:     p.Fix,
		Version: p.Version,
		Facts:   facts,
	}
}

// Best is the strongest level whose driver probed OK, or None when none did.
func Best(probes []Probe) Level {
	best := None
	for _, p := range probes {
		if p.OK && p.Level > best {
			best = p.Level
		}
	}
	return best
}

var (
	driversMu sync.RWMutex
	drivers   = map[string]Driver{}
)

// Register adds a wall driver to this helper's registry.
func Register(d Driver) {
	if d == nil || d.Name() == "" {
		return
	}
	driversMu.Lock()
	drivers[d.Name()] = d
	driversMu.Unlock()
}

// Drivers lists the drivers built into this helper, strongest level first.
func Drivers() []Driver {
	driversMu.RLock()
	out := make([]Driver, 0, len(drivers))
	for _, d := range drivers {
		out = append(out, d)
	}
	driversMu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Level() != out[j].Level() {
			return out[i].Level() > out[j].Level()
		}
		return out[i].Name() < out[j].Name()
	})
	return out
}
