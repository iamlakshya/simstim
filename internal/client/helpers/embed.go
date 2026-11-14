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
)

//go:embed *
var files embed.FS

// read returns a helper by name: from this build, or else from the helpers
// Fetch downloaded.
func read(name string) ([]byte, bool) {
	fetchMu.Lock()
	defer fetchMu.Unlock()
	if b, err := fs.ReadFile(files, name); err == nil && len(b) > 0 {
		return b, true
	}
	b, ok := fetched[name]
	return b, ok && len(b) > 0
}

// HideEmbedded makes this process act as a client built with go install:
// it has no helpers until Fetch loads them. It returns a function that puts
// the embedded helpers back. Only tests call it.
func HideEmbedded() func() {
	fetchMu.Lock()
	savedFiles, savedFetched := files, fetched
	files, fetched = embed.FS{}, nil
	fetchMu.Unlock()
	return func() {
		fetchMu.Lock()
		files, fetched = savedFiles, savedFetched
		fetchMu.Unlock()
	}
}

// fsGlob lists this build's helpers by pattern.
func fsGlob(pattern string) ([]string, error) {
	fetchMu.Lock()
	defer fetchMu.Unlock()
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

// File returns a file of this build's helpers by its name, such as
// host-linux-amd64.
func File(name string) ([]byte, bool) { return read(name) }

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
