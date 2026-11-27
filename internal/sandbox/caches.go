// Package sandbox holds built-in sandbox data and home path checks.
package sandbox

import (
	_ "embed"
	"fmt"
	"net/url"
	"os"
	"slices"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/files"
)

//go:embed caches.toml
var cachesTOML string

// Cache names one built-in cache root and its primary documentation.
type Cache struct {
	Tool   string `toml:"tool"`
	Path   string `toml:"path"`
	Source string `toml:"source"`
}

var loadCaches = sync.OnceValues(func() ([]Cache, error) { return readCaches(cachesTOML) })

func readCaches(data string) ([]Cache, error) {
	var table struct {
		Caches []Cache `toml:"cache"`
	}
	meta, err := toml.Decode(data, &table)
	if err != nil {
		return nil, fmt.Errorf("read built-in caches: %w", err)
	}
	if len(meta.Undecoded()) != 0 || len(table.Caches) == 0 || len(table.Caches) > 64 {
		return nil, fmt.Errorf("built-in cache data has missing or unknown entries")
	}
	var paths []string
	for _, cache := range table.Caches {
		if cache.Tool == "" {
			return nil, fmt.Errorf("cache %q has no tool name", cache.Path)
		}
		if err := ValidateCachePath(cache.Path); err != nil {
			return nil, fmt.Errorf("built-in cache %q: %w", cache.Tool, err)
		}
		source, err := url.Parse(cache.Source)
		if err != nil || source.Scheme != "https" || source.Host == "" || source.User != nil {
			return nil, fmt.Errorf("cache %q has no HTTPS primary source", cache.Tool)
		}
		paths = append(paths, cache.Path)
	}
	if err := (files.StatePlan{Dirs: paths}).Validate(); err != nil {
		return nil, fmt.Errorf("built-in cache roots: %w", err)
	}
	return table.Caches, nil
}

// Caches returns a copy of the built-in cache table. Loading never contacts
// the documentation URLs; they are references for maintainers and users.
func Caches() ([]Cache, error) {
	entries, err := loadCaches()
	return slices.Clone(entries), err
}

// DefaultPaths returns the built-in home-relative cache roots. The policy's
// built-in layer adds them before list merging, so ! removals remain effective.
func DefaultPaths() ([]string, error) {
	entries, err := loadCaches()
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, cache := range entries {
		paths = append(paths, cache.Path)
	}
	return paths, nil
}

// AppendCacheDirs adds an already merged cache selection to a local state
// plan. It never fills an empty selection or changes the persisted base plan.
func AppendCacheDirs(base files.StatePlan, enabled bool, selected []string) (files.StatePlan, error) {
	out := files.StatePlan{Dirs: slices.Clone(base.Dirs), Files: slices.Clone(base.Files)}
	if err := out.Validate(); err != nil {
		return files.StatePlan{}, fmt.Errorf("validate kept state: %w", err)
	}
	for _, name := range selected {
		if err := ValidateCachePath(name); err != nil {
			return files.StatePlan{}, err
		}
	}
	if !enabled {
		return out, nil
	}
	out.Dirs = append(out.Dirs, selected...)
	if err := out.Validate(); err != nil {
		return files.StatePlan{}, fmt.Errorf("cache roots overlap kept state or another cache: %w", err)
	}
	return out, nil
}

// PrepareHome creates only the default cache directories in the guest home.
// Persistence comes from each sandbox's own state bindings, not these mkdirs.
// Disabled or removed roots are ordinary empty home directories for that run.
func PrepareHome(home string) error {
	paths, err := DefaultPaths()
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return fmt.Errorf("open guest home for caches: %w", err)
	}
	defer root.Close()
	for _, name := range paths {
		if err := root.MkdirAll(name, 0700); err != nil {
			return fmt.Errorf("make guest cache %q: %w", name, err)
		}
	}
	return nil
}
