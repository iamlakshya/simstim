// Package helpers embeds the host helper binaries that the client installs on hosts.
//
// The binaries are build output and are not in git. `just helpers` (or the release
// build) writes them here before the client is built. A client built without them
// (go install) fetches them from its release with Fetch, and can still talk to
// hosts that already have a matching helper.
package helpers

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed *
var files embed.FS

// read returns a helper by name: from this build, or else from the helpers
// Fetch downloaded.
func read(name string) ([]byte, bool) {
	if b, err := fs.ReadFile(files, name); err == nil && len(b) > 0 {
		return b, true
	}
	fetchMu.Lock()
	defer fetchMu.Unlock()
	b, ok := fetched[name]
	return b, ok && len(b) > 0
}

// fsGlob lists helpers by pattern: this build's, or else the fetched ones.
func fsGlob(pattern string) ([]string, error) {
	names, err := fs.Glob(files, pattern)
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", pattern, err)
	}
	return names, nil
}

func allNames(pattern string) []string {
	if names, _ := fsGlob(pattern); len(names) > 0 {
		return names
	}
	fetchMu.Lock()
	defer fetchMu.Unlock()
	var names []string
	for n := range fetched {
		if ok, _ := filepath.Match(pattern, n); ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// For returns the helper binary for a host platform, such as "linux", "amd64".
func For(goos, goarch string) ([]byte, bool) { return read("host-" + goos + "-" + goarch) }

// VT returns the shadow-terminal binary for a host platform, if this build has it.
func VT(goos, goarch string) ([]byte, bool) { return read("vt-" + goos + "-" + goarch) }

// Version identifies this set of helpers. It changes whenever any helper changes,
// so each build installs into its own folder on the host and running sessions
// keep their own binary.
func Version() string {
	names := append(allNames("host-*"), allNames("vt-*")...)
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		b, _ := read(n)
		h.Write([]byte(n))
		h.Write(b)
	}
	if len(names) == 0 {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// Platforms lists the host platforms there are helpers for.
func Platforms() []string { return allNames("host-*") }

// HelperDigest names one host helper binary and its expected SHA-256 digest
// (lowercase hex).
type HelperDigest struct {
	OS     string
	Arch   string
	SHA256 string
}

// DigestFor returns the expected SHA-256 digest (lowercase hex) of the host
// helper for goos/goarch, from this build's embedded helpers or the cache.
func DigestFor(goos, goarch string) (string, bool) {
	b, ok := For(goos, goarch)
	if !ok {
		return "", false
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), true
}

// Digests returns the expected SHA-256 digests of every host helper this build
// embeds or has loaded from the cache, sorted by platform.
func Digests() []HelperDigest {
	names := allNames("host-*")
	out := make([]HelperDigest, 0, len(names))
	for _, n := range names {
		rest, ok := strings.CutPrefix(n, "host-")
		if !ok {
			continue
		}
		goos, goarch, ok := strings.Cut(rest, "-")
		if !ok || goos == "" || goarch == "" {
			continue
		}
		sum, ok := DigestFor(goos, goarch)
		if !ok {
			continue
		}
		out = append(out, HelperDigest{OS: goos, Arch: goarch, SHA256: sum})
	}
	return out
}
