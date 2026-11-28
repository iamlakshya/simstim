// Package sandbox holds shared sandbox data tables and path validation used
// by the client, host helper, wall drivers and guest init.
package sandbox

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"

	tomlv2 "github.com/pelletier/go-toml/v2"
)

//go:embed caches.toml
var cachesTOML []byte

// CacheEntry describes one built-in package-manager or build cache directory
// relative to /home/agent, together with its upstream documentation URL.
type CacheEntry struct {
	Tool   string `toml:"tool"`
	Path   string `toml:"path"`
	Source string `toml:"source"`
	Notes  string `toml:"notes"`
}

type cachesFile struct {
	Caches []CacheEntry `toml:"caches"`
}

// refusedStartupPaths lists shell startup, hook and git config paths that must
// never persist across sandbox restarts (isolation spec 6.11, 7.1, 17.5).
var refusedStartupPaths = map[string]bool{
	".bashrc":                     true,
	".profile":                    true,
	".bash_profile":               true,
	".bash_login":                 true,
	".bash_logout":                true,
	".zshrc":                      true,
	".zprofile":                   true,
	".zshenv":                     true,
	".zlogin":                     true,
	".zlogout":                    true,
	".claude/settings.json":       true,
	".claude/settings.local.json": true,
	".gitconfig":                  true,
	".config/git":                 true,
}

// refusedExecRoots lists folders that hold executables or toolchains (spec 17.5).
// Neither these paths nor anything inside them may be kept as a cache.
var refusedExecRoots = []string{
	".cargo/bin",
	"go/bin",
	".local/bin",
	".yarn/bin",
	".npm-global",
	".pnpm-global",
	".nvm",
	".rustup",
	".volta",
	".fnm",
	".sdkman",
	".pyenv",
	".rbenv",
	".asdf",
	".mise",
}

// refusedExecAncestors lists parent folders that contain an executable or
// configuration subtree, so mounting the whole parent would silently keep
// executables (for example ".cargo" contains ".cargo/bin").
var refusedExecAncestors = map[string]string{
	".cargo":  ".cargo/bin",
	"go":      "go/bin",
	".local":  ".local/bin",
	".yarn":   ".yarn/bin",
	".config": ".config/git",
}

// refusedCredentialRoots lists credential or secret folders/files that must
// never be persisted as caches.
var refusedCredentialRoots = []string{
	".ssh",
	".gnupg",
	".aws",
	".kube",
	".docker",
	".netrc",
	".git-credentials",
	".npmrc",
	".pypirc",
	".cargo/credentials",
	".cargo/credentials.toml",
}

var (
	cachesOnce    sync.Once
	cachedEntries []CacheEntry
	errCachesLoad error
)

// DefaultCaches returns the built-in kept cache entries from caches.toml.
func DefaultCaches() ([]CacheEntry, error) {
	cachesOnce.Do(func() {
		var f cachesFile
		dec := tomlv2.NewDecoder(bytes.NewReader(cachesTOML))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			errCachesLoad = fmt.Errorf("parse caches.toml: %w", err)
			return
		}
		if len(f.Caches) == 0 {
			errCachesLoad = errors.New("caches.toml lists no caches")
			return
		}
		seen := make(map[string]bool, len(f.Caches))
		for _, e := range f.Caches {
			if strings.TrimSpace(e.Tool) == "" {
				errCachesLoad = fmt.Errorf("caches.toml entry %q is missing tool", e.Path)
				return
			}
			if !strings.HasPrefix(e.Source, "https://") {
				errCachesLoad = fmt.Errorf("caches.toml entry %q must have an https:// source URL", e.Path)
				return
			}
			clean, err := ValidateCachePath(e.Path)
			if err != nil {
				errCachesLoad = fmt.Errorf("caches.toml entry %q: %w", e.Path, err)
				return
			}
			if seen[clean] {
				errCachesLoad = fmt.Errorf("caches.toml duplicates cache path %q", clean)
				return
			}
			seen[clean] = true
			e.Path = clean
			cachedEntries = append(cachedEntries, e)
		}
	})
	if errCachesLoad != nil {
		return nil, errCachesLoad
	}
	return slices.Clone(cachedEntries), nil
}

// DefaultCachePaths returns the ordered relative paths from caches.toml.
func DefaultCachePaths() []string {
	entries, err := DefaultCaches()
	if err != nil {
		return nil
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Path
	}
	return out
}

// ValidateCachePath checks that rel is a safe cache directory relative to
// /home/agent, refusing absolute paths, traversal, startup files, executable
// folders (such as .cargo/bin) and credential paths. It returns the clean
// POSIX relative path.
func ValidateCachePath(rel string) (string, error) {
	clean, err := validateCleanRelPath(rel)
	if err != nil {
		return "", err
	}
	if refusedStartupPaths[clean] || hasPrefixPath(clean, ".config/git") {
		return "", fmt.Errorf("cache path %q is a startup or hook file and cannot be kept. Try: list package or build cache folders only", rel)
	}
	if child, ok := refusedExecAncestors[clean]; ok {
		return "", fmt.Errorf("cache path %q contains executable or config folder %q and cannot be kept. Try: name a cache subfolder instead", rel, child)
	}
	for _, root := range refusedExecRoots {
		if hasPrefixPath(clean, root) {
			return "", fmt.Errorf("cache path %q is an executable folder and cannot be kept. Try: remove %q from sandbox.caches", rel, rel)
		}
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == "bin" || seg == "sbin" || seg == "shims" {
			return "", fmt.Errorf("cache path %q names an executable folder (%q) and cannot be kept. Try: keep cache data folders only", rel, seg)
		}
	}
	for _, root := range refusedCredentialRoots {
		if hasPrefixPath(clean, root) {
			return "", fmt.Errorf("cache path %q holds credentials or config and cannot be kept as a cache. Try: remove %q from sandbox.caches", rel, rel)
		}
	}
	return clean, nil
}

// ParseCacheRule parses one sandbox.caches rule: either a relative path to add,
// or "!<path>" to drop a path from the built-in list or a wider layer.
func ParseCacheRule(raw string) (clean string, drop bool, err error) {
	if raw == "" || strings.TrimSpace(raw) == "" {
		return "", false, errors.New("cache rule cannot be empty. Try: use a relative path under /home/agent, such as \".cache/pip\" or \"!.npm/_cacache\"")
	}
	if rest, ok := strings.CutPrefix(raw, "!"); ok {
		c, err := validateCleanRelPath(rest)
		if err != nil {
			return "", true, fmt.Errorf("invalid drop rule %q: %w", raw, err)
		}
		return c, true, nil
	}
	c, err := ValidateCachePath(raw)
	if err != nil {
		return "", false, err
	}
	return c, false, nil
}

// ValidateCacheRules validates every rule in rules.
func ValidateCacheRules(rules []string) error {
	for _, r := range rules {
		if _, _, err := ParseCacheRule(r); err != nil {
			return err
		}
	}
	return nil
}

// ResolveCaches computes the final ordered list of relative cache directories
// for a sandbox. Every rule in layers is validated even when keep is false so
// configuration errors fail closed. Layers are applied from widest to
// narrowest on top of caches.toml.
func ResolveCaches(keep bool, layers ...[]string) ([]string, error) {
	for _, layer := range layers {
		if err := ValidateCacheRules(layer); err != nil {
			return nil, err
		}
	}
	if !keep {
		return nil, nil
	}
	base, err := DefaultCaches()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(base))
	for i, e := range base {
		out[i] = e.Path
	}
	for _, layer := range layers {
		for _, raw := range layer {
			clean, drop, err := ParseCacheRule(raw)
			if err != nil {
				return nil, err
			}
			if drop {
				out = slices.DeleteFunc(out, func(p string) bool { return p == clean })
				continue
			}
			if !slices.Contains(out, clean) {
				out = append(out, clean)
			}
		}
	}
	return out, nil
}

// SortMountOrder returns a copy of paths sorted with shallower directories
// before deeper ones (then lexicographically) so parent mounts precede children.
func SortMountOrder(paths []string) []string {
	out := slices.Clone(paths)
	slices.SortFunc(out, func(a, b string) int {
		da := strings.Count(a, "/")
		db := strings.Count(b, "/")
		if da != db {
			return da - db
		}
		return strings.Compare(a, b)
	})
	return out
}

func validateCleanRelPath(rel string) (string, error) {
	if rel == "" || strings.TrimSpace(rel) == "" {
		return "", errors.New("cache path cannot be empty. Try: use a relative path under /home/agent, such as \".cache/pip\"")
	}
	if rel != strings.TrimSpace(rel) {
		return "", fmt.Errorf("cache path %q has leading or trailing whitespace. Try: remove the spaces", rel)
	}
	for _, r := range rel {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("cache path %q contains a control character. Try: use a plain relative path", rel)
		}
	}
	if strings.Contains(rel, "\\") {
		return "", fmt.Errorf("cache path %q must use forward slashes. Try: replace backslashes with \"/\"", rel)
	}
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "~") {
		return "", fmt.Errorf("cache path %q must be relative to /home/agent. Try: drop the leading %q", rel, rel[:1])
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return "", fmt.Errorf("cache path %q must not contain \"..\". Try: use a path inside /home/agent", rel)
		}
	}
	clean := path.Clean(rel)
	if clean == "." || clean == "" || clean == ".." || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("cache path %q is not a subfolder of /home/agent. Try: name a cache folder such as \".cache/pip\"", rel)
	}
	return clean, nil
}

func hasPrefixPath(clean, root string) bool {
	return clean == root || strings.HasPrefix(clean, root+"/")
}
