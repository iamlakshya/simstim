package wall

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/paths"
)

// ProbeAll runs every registered driver's probe, strongest level first.
func ProbeAll(ctx context.Context) []Probe {
	ds := Drivers()
	out := make([]Probe, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Probe(ctx))
	}
	return out
}

// ToAPIProbes turns a slice of Probe values into wire api.WallProbe values.
// The returned slice is always non-nil, so JSON output never holds null.
func ToAPIProbes(probes []Probe) []api.WallProbe {
	out := make([]api.WallProbe, 0, len(probes))
	for _, p := range probes {
		out = append(out, p.ToAPI())
	}
	return out
}

// cachedProbes is the on-disk format of StateDir()/walls.json (isolation spec 5.1).
type cachedProbes struct {
	BootID        string  `json:"boot_id"`
	HelperVersion string  `json:"helper_version"`
	Probes        []Probe `json:"probes"`
}

// ReadCache reads cached driver probes from path when both bootID and
// helperVersion match the cached record.
func ReadCache(path, bootID, helperVersion string) ([]Probe, bool) {
	if helperVersion == "" {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var c cachedProbes
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, false
	}
	if c.HelperVersion != helperVersion || c.BootID != bootID {
		return nil, false
	}
	if c.Probes == nil {
		return []Probe{}, true
	}
	return c.Probes, true
}

// WriteCache atomically writes probes to path, keyed by bootID and
// helperVersion.
func WriteCache(path, bootID, helperVersion string, probes []Probe) error {
	if probes == nil {
		probes = []Probe{}
	}
	dir := filepath.Dir(path)
	if err := paths.EnsurePrivateDir(dir); err != nil {
		return fmt.Errorf("prepare probe cache directory: %w", err)
	}
	b, err := json.MarshalIndent(cachedProbes{
		BootID:        bootID,
		HelperVersion: helperVersion,
		Probes:        probes,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode probe cache: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".walls-*")
	if err != nil {
		return fmt.Errorf("create temp probe cache: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("chmod temp probe cache: %w", err)
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("write probe cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("close probe cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("rename probe cache: %w", err)
	}
	return nil
}
