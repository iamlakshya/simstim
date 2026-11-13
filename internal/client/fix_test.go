package client

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/testhome"
)

func TestLocalFixes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the client shares no SSH connections on Windows, so it has no control sockets to fix")
	}
	cfg, cache := t.TempDir(), shortDir(t)
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", cache)
	if err := SaveHosts(context.Background(), []Host{{Name: "kept", SSH: "kept"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := writeWrapper(Host{Name: "kept", SSH: "kept"}); err != nil {
		t.Fatal(err)
	}
	if _, err := writeWrapper(Host{Name: "gone", SSH: "gone"}); err != nil {
		t.Fatal(err)
	}
	cm := filepath.Join(CacheDir(), "cm")
	_ = os.MkdirAll(cm, 0o700)
	live, err := net.Listen("unix", filepath.Join(cm, "live"))
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	dead, err := net.Listen("unix", filepath.Join(cm, "dead"))
	if err != nil {
		t.Fatal(err)
	}
	dead.(*net.UnixListener).SetUnlinkOnClose(false)
	dead.Close()

	fixes := LocalFixes(false, nil)
	if len(fixes) != 2 || fixes[0].ID != "ssh-configs" || fixes[1].ID != "control-sockets" {
		t.Fatalf("fixes %+v", fixes)
	}
	if !strings.HasSuffix(fixes[0].Command, "config-gone") || !strings.HasSuffix(fixes[1].Command, "/dead") {
		t.Fatalf("commands %q %q", fixes[0].Command, fixes[1].Command)
	}
	for _, f := range LocalFixes(true, []string{"ssh-configs", "control-sockets"}) {
		if !f.Done {
			t.Errorf("%s: %+v", f.ID, f)
		}
	}
	for p, want := range map[string]bool{
		wrapperPath(Host{Name: "kept"}): true, wrapperPath(Host{Name: "gone"}): false,
		filepath.Join(cm, "live"): true, filepath.Join(cm, "dead"): false,
	} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s kept = %v, want %v", p, err == nil, want)
		}
	}
	if left := LocalFixes(false, nil); len(left) != 0 {
		t.Errorf("left to fix: %+v", left)
	}
}

// A cancel stops the local fixes: with ctx ended, ApplyLocalFixes removes
// no file, marks no fix done, and returns ctx's error.
func TestLocalFixesStopAtACancel(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if _, err := writeWrapper(Host{Name: "gone", SSH: "gone"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fixes, err := ApplyLocalFixes(ctx, []string{"ssh-configs"})
	if !errors.Is(err, context.Canceled) || len(fixes) != 1 || fixes[0].Done {
		t.Errorf("fixes after a cancel: %+v, %v", fixes, err)
	}
	if _, err := os.Stat(wrapperPath(Host{Name: "gone"})); err != nil {
		t.Errorf("a fix ran after the cancel: %v", err)
	}
}

// cancelAfterFirst cancels ctx once a fix has removed its first file.
func cancelAfterFirst(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	old := RemovedFile
	RemovedFile = func(string) { cancel() }
	t.Cleanup(func() { RemovedFile = old; cancel() })
	return ctx
}

// A cancel after a fix's first file keeps its other files: the fix looks
// at the cancel before each one.
func TestACancelBetweenTwoFilesKeepsTheSecond(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, name := range []string{"gone1", "gone2"} {
		if _, err := writeWrapper(Host{Name: name, SSH: name}); err != nil {
			t.Fatal(err)
		}
	}
	fixes, err := ApplyLocalFixes(cancelAfterFirst(t), []string{"ssh-configs"})
	if !errors.Is(err, context.Canceled) || len(fixes) != 1 || fixes[0].Done {
		t.Errorf("a cancel after the first file: %+v, %v", fixes, err)
	}
	if _, err := os.Stat(wrapperPath(Host{Name: "gone1"})); err == nil {
		t.Error("the first file is still there")
	}
	if want := []string{"removed " + wrapperPath(Host{Name: "gone1"})}; len(fixes) == 1 && !slices.Equal(fixes[0].Changed, want) {
		t.Errorf("the changes of a fix that a cancel stopped: %q, want %q", fixes[0].Changed, want)
	}
	if _, err := os.Stat(wrapperPath(Host{Name: "gone2"})); err != nil {
		t.Errorf("the second file went after the cancel: %v", err)
	}
}

// A cancel after the first of two fixes keeps the second from starting:
// ApplyLocalFixes looks at the cancel before each fix, so the second one
// has neither a result nor an error.
func TestACancelBetweenTwoFixesStartsNoSecond(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the client shares no SSH connections on Windows, so it has no control sockets to fix")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", shortDir(t))
	if _, err := writeWrapper(Host{Name: "gone", SSH: "gone"}); err != nil {
		t.Fatal(err)
	}
	cm := filepath.Join(CacheDir(), "cm")
	if err := os.MkdirAll(cm, 0o700); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(cm, "dead")
	l, err := net.Listen("unix", dead)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
	fixes, err := ApplyLocalFixes(cancelAfterFirst(t), []string{"ssh-configs", "control-sockets"})
	if !errors.Is(err, context.Canceled) || len(fixes) != 2 {
		t.Fatalf("a cancel between two fixes: %+v, %v", fixes, err)
	}
	if first := fixes[0]; first.ID != "ssh-configs" || !first.Done {
		t.Errorf("the first fix: %+v", first)
	}
	if second := fixes[1]; second.ID != "control-sockets" || second.Done || second.Error != "" {
		t.Errorf("the second fix started after the cancel: %+v", second)
	}
	if _, err := os.Stat(dead); err != nil {
		t.Errorf("the second fix removed its socket after the cancel: %v", err)
	}
}

func TestLocalChecks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_ = os.MkdirAll(ConfigDir(), 0o700)
	conf := "[ui]\nmotoin = \"off\"\n\n[secrets]\nA_KEY = \"no-such-program-here read x\"\nB_KEY = \"sh -c 'echo b'\"\n"
	if err := os.WriteFile(settingsPath(), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	got := map[string]Check{}
	for _, c := range LocalChecks() {
		got[c.Name] = c
	}
	if c := got["config.toml"]; c.OK || !strings.Contains(c.Detail, "ui.motoin") {
		t.Errorf("config.toml: %+v", c)
	}
	if c := got["hosts.toml"]; !c.OK {
		t.Errorf("hosts.toml: %+v", c)
	}
	if c := got["secret A_KEY"]; c.OK || !strings.Contains(c.Detail, "no-such-program-here is not on PATH") {
		t.Errorf("A_KEY: %+v", c)
	}
	if c := got["secret B_KEY"]; !c.OK {
		t.Errorf("B_KEY: %+v", c)
	}
}

// shortDir is a folder with a path short enough for sockets on macOS.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := testhome.ShortTempDir("fx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
