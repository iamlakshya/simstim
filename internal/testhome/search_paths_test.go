package testhome

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const searchPathChildEnv = "TESTHOME_SEARCH_CHILD"

var searchPathNames = []string{"XDG_CONFIG_DIRS", "XDG_DATA_DIRS"}

var searchHomeNames = []string{
	"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME",
	"XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR",
}

type searchChildWant struct {
	Trusted   bool
	Root      string
	Home      map[string]string
	GoFolders map[string]string
	Sentinels []string
}

type searchChildReport struct {
	Root string
	Home string
}

// This child runs after TestMain called Isolate. It checks the environment
// that a real child test binary receives, including the trusted-root branch.
func TestSearchPathsInAChild(t *testing.T) {
	encoded := os.Getenv(searchPathChildEnv)
	if encoded == "" {
		return
	}
	var want searchChildWant
	if err := json.Unmarshal([]byte(encoded), &want); err != nil {
		t.Fatal(err)
	}
	root, home := Root(), os.Getenv("HOME")
	if !trusted(root) {
		t.Fatalf("child root %q is not owned by testhome", root)
	}
	if want.Trusted {
		if root != want.Root {
			t.Errorf("trusted child moved its root from %q to %q", want.Root, root)
		}
		for name, value := range want.Home {
			if got := os.Getenv(name); got != value {
				t.Errorf("trusted child changed %s from %q to %q", name, value, got)
			}
		}
	} else {
		if root == want.Root {
			t.Errorf("fresh child kept the inherited root %q", root)
		}
		for _, name := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR"} {
			if got := os.Getenv(name); !under(root, got) {
				t.Errorf("fresh child has %s=%q outside its root %q", name, got, root)
			}
		}
	}
	for name, value := range want.GoFolders {
		if got := os.Getenv(name); got != value {
			t.Errorf("child moved %s from %q to %q", name, value, got)
		}
	}
	for _, name := range searchPathNames {
		value := os.Getenv(name)
		paths := filepath.SplitList(value)
		if value == "" || len(paths) == 0 {
			t.Errorf("child left %s empty", name)
		}
		for _, path := range paths {
			if path == "" || !filepath.IsAbs(path) || !under(root, path) || under(home, path) {
				t.Errorf("child has %s=%q, want an absolute owned directory outside the home", name, path)
				continue
			}
			st, err := os.Stat(path)
			if err != nil || !st.IsDir() {
				t.Errorf("child search directory %q was not created: %v", path, err)
				continue
			}
			if runtime.GOOS != "windows" && st.Mode().Perm() != 0o700 {
				t.Errorf("child search directory %q has mode %o, want 700", path, st.Mode().Perm())
			}
			// Look only through the child's effective search paths. The
			// inherited external sentinels must be unreachable from them.
			for _, sentinel := range want.Sentinels {
				if _, err := os.Stat(filepath.Join(path, sentinel)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("child found an inherited sentinel through %s: %s, %v", name, sentinel, err)
				}
			}
		}
	}
	report, err := json.Marshal(searchChildReport{Root: root, Home: home})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("search-path-report=%s\n", report)
}

type searchSentinel struct {
	dir     string
	path    string
	data    string
	mod     time.Time
	dirMode os.FileMode
}

func makeSearchSentinels(t *testing.T) ([]searchSentinel, map[string]string) {
	t.Helper()
	var sentinels []searchSentinel
	values := map[string]string{}
	for _, name := range searchPathNames {
		var dirs []string
		for _, suffix := range []string{"first", "second"} {
			dir := filepath.Join(t.TempDir(), strings.ToLower(name)+"-"+suffix)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "external-"+strings.ToLower(name)+"-"+suffix)
			data := "these settings belong to the inherited search path\n"
			if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			st, err := os.Stat(file)
			if err != nil {
				t.Fatal(err)
			}
			d, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			sentinels = append(sentinels, searchSentinel{dir: dir, path: file, data: data, mod: st.ModTime(), dirMode: d.Mode()})
			dirs = append(dirs, dir)
		}
		values[name] = strings.Join(dirs, string(os.PathListSeparator))
	}
	return sentinels, values
}

func checkSearchSentinels(t *testing.T, sentinels []searchSentinel) {
	t.Helper()
	for _, sentinel := range sentinels {
		data, err := os.ReadFile(sentinel.path)
		if err != nil || string(data) != sentinel.data {
			t.Errorf("child changed inherited settings %s: %q, %v", sentinel.path, data, err)
		}
		st, err := os.Stat(sentinel.path)
		if err != nil || !st.ModTime().Equal(sentinel.mod) {
			t.Errorf("child replaced inherited settings %s: %v", sentinel.path, err)
		}
		d, err := os.Stat(sentinel.dir)
		if err != nil || d.Mode() != sentinel.dirMode {
			t.Errorf("child changed inherited directory %s: %v", sentinel.dir, err)
		}
		entries, err := os.ReadDir(sentinel.dir)
		if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(sentinel.path) {
			t.Errorf("child changed inherited directory entries in %s: %v, %v", sentinel.dir, entries, err)
		}
	}
}

func searchDirectoryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name()
	}
	return names
}

// Untrusted children get a new root. Trusted children keep their home, even
// when a caller moved it, but must still repair inherited search paths. Each
// child starts with multi-entry paths that contain real, owned test sentinels.
func TestIsolationOwnsInheritedSearchPaths(t *testing.T) {
	for _, mode := range []string{"unset-root", "untrusted-root", "trusted-root"} {
		t.Run(mode, func(t *testing.T) {
			sentinels, contaminated := makeSearchSentinels(t)
			quiet, ok := goQuiet()
			if !ok {
				t.Fatal("could not prepare an owned config directory for the child's go command")
			}
			want := searchChildWant{Trusted: mode == "trusted-root", Root: Root(), Home: map[string]string{}, GoFolders: goFolders()}
			for _, name := range searchHomeNames {
				want.Home[name] = os.Getenv(name)
			}
			if want.Trusted {
				// hosttest children may deliberately move their home while
				// keeping a genuine root. Repair only the search paths.
				moved := t.TempDir()
				want.Home["HOME"], want.Home["USERPROFILE"] = moved, moved
				file := filepath.Join(moved, "kept-home")
				const data = "keep this child's existing home\n"
				if err := os.WriteFile(file, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
				st, err := os.Stat(file)
				if err != nil {
					t.Fatal(err)
				}
				d, err := os.Stat(moved)
				if err != nil {
					t.Fatal(err)
				}
				sentinels = append(sentinels, searchSentinel{dir: moved, path: file, data: data, mod: st.ModTime(), dirMode: d.Mode()})
			}
			for _, sentinel := range sentinels {
				want.Sentinels = append(want.Sentinels, filepath.Base(sentinel.path))
			}
			parentDirs := map[string][]string{}
			for _, dir := range []string{Root(), os.Getenv("HOME"), want.Home["HOME"]} {
				parentDirs[dir] = searchDirectoryNames(t, dir)
			}
			encoded, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSearchPathsInAChild$", "-test.v")
			for _, kv := range os.Environ() {
				name, _, _ := strings.Cut(kv, "=")
				if name != rootVar && name != searchPathChildEnv && name != "XDG_CONFIG_DIRS" && name != "XDG_DATA_DIRS" {
					cmd.Env = append(cmd.Env, kv)
				}
			}
			if want.Trusted {
				for name, value := range want.Home {
					cmd.Env = append(cmd.Env, name+"="+value)
				}
			} else {
				// Before fresh isolation, TestMain asks go env about its
				// folders. Its config is owned and telemetry is off there.
				cmd.Env = append(cmd.Env, quiet...)
			}
			switch mode {
			case "untrusted-root":
				cmd.Env = append(cmd.Env, rootVar+"="+filepath.Join(t.TempDir(), "untrusted"))
			case "trusted-root":
				cmd.Env = append(cmd.Env, rootVar+"="+Root())
			}
			for name, value := range want.GoFolders {
				cmd.Env = append(cmd.Env, name+"="+value)
			}
			for name, value := range contaminated {
				cmd.Env = append(cmd.Env, name+"="+value)
			}
			cmd.Env = append(cmd.Env, searchPathChildEnv+"="+string(encoded))
			out, runErr := cmd.CombinedOutput()
			if runErr != nil {
				t.Errorf("%s child: %v\n%s", mode, runErr, out)
			}
			var report searchChildReport
			for _, line := range strings.Split(string(out), "\n") {
				if data, ok := strings.CutPrefix(line, "search-path-report="); ok {
					if err := json.Unmarshal([]byte(data), &report); err != nil {
						t.Errorf("child report: %v", err)
					}
				}
			}
			if report.Root == "" {
				t.Errorf("%s child reported no root:\n%s", mode, out)
			} else if want.Trusted {
				if report.Root != Root() || report.Home != want.Home["HOME"] {
					t.Errorf("trusted child moved root or home: %+v", report)
				}
			} else if _, err := os.Stat(report.Root); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("fresh child's root survived its exit: %s, %v", report.Root, err)
			}
			checkSearchSentinels(t, sentinels)
			for dir, names := range parentDirs {
				if got := searchDirectoryNames(t, dir); !slices.Equal(got, names) {
					t.Errorf("child changed parent directory %s: %q, want %q", dir, got, names)
				}
			}
		})
	}
}
