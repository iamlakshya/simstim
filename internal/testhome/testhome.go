// Package testhome gives a test binary a home folder of its own. Before
// the tests run, HOME, USERPROFILE and the XDG folders point into a new
// temporary folder, so no test reads or writes the user's real ones, such
// as the state folder and its debug folder. The runtime folder moves too,
// so no test uses the sockets' folder that the user's client and other
// test runs share. The folder goes after the tests. Go's own folders keep
// their places, so a build inside a test finds its caches. Only tests
// import it.
package testhome

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

// rootVar holds the temporary folder, for the processes that the tests
// start. A test binary that a test starts again keeps the home it got.
const rootVar = "TESTHOME_ROOT"

// marker is the file that Isolate puts in each folder that it makes. A
// test binary trusts rootVar only for a folder that holds it.
const marker = ".testhome"

// maxRoot is the longest root whose sockets fit in the 103 bytes that a
// socket path has on macOS. The longest that a test makes in it is the one
// of a shared SSH connection, in the cache folder: the root,
// "/home/.cache/<name>/cm/nf-", and the 40 characters of OpenSSH's %C. A
// session's socket in the runtime folder is shorter.
var maxRoot = 103 - len("/home/.cache/") - len(brand.Name) - len("/cm/nf-") - 40

// Main runs the tests in a home of their own, removes it, and exits with
// the tests' code. Call it from TestMain.
func Main(m *testing.M) {
	done, err := Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	done()
	os.Exit(code)
}

// Isolate points HOME, USERPROFILE, XDG_CONFIG_HOME, XDG_DATA_HOME,
// XDG_STATE_HOME, XDG_CACHE_HOME, XDG_RUNTIME_DIR, XDG_CONFIG_DIRS and
// XDG_DATA_DIRS into a new temporary folder, and returns a function that removes
// it. GOCACHE, GOMODCACHE, GOPATH and GOENV
// keep the values they had with the old home, set outright. In a test
// binary that a test started, which has a home of its own already, it
// keeps its home: only when rootVar names a folder that Isolate made
// (trusted). The home may be elsewhere then, as a test that starts the
// binary again may have moved it on purpose (hosttest.New). Any other
// value of rootVar, such as a stale one, gets a new home. The two search
// paths always reset to directories inside the tests' root.
func Isolate() (func(), error) {
	if trusted(Root()) {
		if err := isolateSearchPaths(Root()); err != nil {
			return nil, err
		}
		return func() {}, nil
	}
	keep := goFolders()
	root, err := tempDir()
	if err != nil {
		return nil, fmt.Errorf("make a home for the tests: %w", err)
	}
	if err := os.WriteFile(filepath.Join(root, marker), nil, 0o600); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("make a home for the tests: %w", err)
	}
	// The runtime folder is made now: it counts only when it is there.
	home, run := filepath.Join(root, "home"), filepath.Join(root, "run")
	for _, d := range []string{home, run} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			_ = os.RemoveAll(root)
			return nil, fmt.Errorf("make a home for the tests: %w", err)
		}
	}
	set := map[string]string{
		rootVar:           root,
		"HOME":            home,
		"USERPROFILE":     home,
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME":  filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_RUNTIME_DIR": run,
	}
	maps.Copy(set, keep)
	for k, v := range set {
		if err := os.Setenv(k, v); err != nil {
			_ = os.RemoveAll(root)
			return nil, fmt.Errorf("make a home for the tests: %w", err)
		}
	}
	if err := isolateSearchPaths(root); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return func() { _ = os.RemoveAll(root) }, nil
}

func isolateSearchPaths(root string) error {
	for name, dir := range map[string]string{
		"XDG_CONFIG_DIRS": filepath.Join(root, "config-dirs"),
		"XDG_DATA_DIRS":   filepath.Join(root, "data-dirs"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("make the tests' search folder: %w", err)
		}
		if err := os.Setenv(name, dir); err != nil {
			return fmt.Errorf("set the tests' search path: %w", err)
		}
	}
	return nil
}

// Root is the temporary folder that holds the tests' home, or "" before
// Isolate.
func Root() string { return os.Getenv(rootVar) }

// Check fails the test unless Root is a folder that Isolate made, and the
// home folder, and each of dirs, is in it.
func Check(t testing.TB, dirs ...string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if !trusted(Root()) {
		t.Errorf("the tests' folder %q is not one that testhome made", Root())
	}
	for _, d := range append([]string{home}, dirs...) {
		if !under(Root(), d) {
			t.Errorf("%s is not in the tests' own folder %q", d, Root())
		}
	}
}

// trusted says whether root is a folder that Isolate made: a folder th…
// in a temporary folder, which holds the marker.
func trusted(root string) bool {
	if root == "" || !strings.HasPrefix(filepath.Base(root), "th") {
		return false
	}
	if _, err := os.Stat(filepath.Join(root, marker)); err != nil {
		return false
	}
	parent := filepath.Dir(root)
	for _, tmp := range []string{os.TempDir(), "/tmp"} {
		if dir, err := filepath.EvalSymlinks(tmp); err == nil && dir == parent {
			return true
		}
	}
	return false
}

// under says whether path is in root.
func under(root, path string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// goFolders is where Go's own folders are with the home as it is now: from
// the environment, then from the file that `go env -w` writes, then Go's
// defaults.
func goFolders() map[string]string {
	home, _ := os.UserHomeDir()
	cache, _ := os.UserCacheDir()
	config, _ := os.UserConfigDir()
	file := map[string]string{}
	envFile := os.Getenv("GOENV")
	if envFile == "" && config != "" {
		envFile = filepath.Join(config, "go", "env")
	}
	if envFile != "" && envFile != "off" {
		if b, err := os.ReadFile(envFile); err == nil { //nolint:gosec // G304: Go's own settings file, as the go command reads it
			for line := range strings.Lines(string(b)) {
				if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
					file[k] = v
				}
			}
		}
	}
	get := func(k string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return file[k]
	}
	gopath := get("GOPATH")
	if gopath == "" && home != "" {
		gopath = filepath.Join(home, "go")
	}
	modcache := get("GOMODCACHE")
	if modcache == "" && gopath != "" {
		modcache = filepath.Join(filepath.SplitList(gopath)[0], "pkg", "mod")
	}
	gocache := get("GOCACHE")
	if gocache == "" && cache != "" {
		gocache = filepath.Join(cache, "go-build")
	}
	out := map[string]string{}
	for k, v := range map[string]string{"GOENV": envFile, "GOPATH": gopath, "GOMODCACHE": modcache, "GOCACHE": gocache} {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// tempDir makes a new temporary folder th… with a short path, whose
// sockets fit (maxRoot). A long temporary folder, as macOS gives, makes
// way for /tmp. Windows has no such limit.
func tempDir() (string, error) { return tempDirIn("", "/tmp", "th") }

// ShortTempDir makes a new temporary folder, named prefix and a number,
// with a path short enough for the sockets that a test makes in it, as the
// tests' home has (maxRoot). The caller removes it.
func ShortTempDir(prefix string) (string, error) { return tempDirIn("", "/tmp", prefix) }

// tempDirIn makes the folder in base, or else in short. It measures the
// path with its symlinks resolved, as it returns it: /tmp on macOS is
// /private/tmp.
func tempDirIn(base, short, prefix string) (string, error) {
	dir, err := makeTemp(base, prefix)
	if err != nil || len(dir) <= maxRoot || runtime.GOOS == "windows" {
		return dir, err
	}
	_ = os.RemoveAll(dir)
	if dir, err = makeTemp(short, prefix); err != nil {
		return "", err
	}
	if len(dir) > maxRoot {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("make a temporary folder: %s is too long for a socket path in it", dir)
	}
	return dir, nil
}

// makeTemp makes a new folder in base, named prefix and a random suffix, and
// returns its path with its symlinks resolved.
func makeTemp(base, prefix string) (string, error) {
	if base == "" {
		base = os.TempDir()
	}
	resolvedBase, resolveErr := filepath.EvalSymlinks(base)
	nameStart := len(resolvedBase) + 1 + len(prefix)
	var dir string
	var err error
	if runtime.GOOS != "windows" && resolveErr == nil && nameStart+10 > maxRoot && nameStart+compactSuffixLen <= maxRoot {
		dir, err = compactTemp(base, prefix, compactSuffix)
	} else {
		dir, err = os.MkdirTemp(base, prefix)
	}
	if err != nil {
		return "", fmt.Errorf("make a temporary folder: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("find the temporary folder: %w", err)
	}
	return resolved, nil
}
