package host

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
)

// fixHome is a host with five helper versions, oldest first, a current link
// that points at a removed one, a session that runs the oldest, and one
// stale socket next to a live one.
func fixHome(t *testing.T) (live string, stale string) {
	t.Helper()
	secretHome(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	vers := []string{"v1", "v2", "v3", "v4", "v5"}
	for i, v := range vers {
		dir := filepath.Join(paths.BinDir(), v)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dir, brand.HostBinary), []byte("#!/bin/sh\n"), 0o755)
		at := time.Now().Add(time.Duration(i-len(vers)) * time.Hour)
		_ = os.Chtimes(dir, at, at)
	}
	if err := os.Symlink("v0", filepath.Join(paths.BinDir(), "current")); err != nil {
		t.Fatal(err)
	}
	old := selfExe
	selfExe = func() (string, error) { return filepath.Join(paths.BinDir(), "v5", brand.HostBinary), nil }
	t.Cleanup(func() { selfExe = old })

	// A running session whose holder is the oldest version.
	live = NewID()
	m := &Manifest{Schema: 1, ID: live, Name: "old", HolderBinary: filepath.Join(paths.BinDir(), "v1", brand.HostBinary)}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsurePrivateDir(filepath.Dir(paths.Socket(live))); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", paths.Socket(live))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	// A socket whose holder died without removing it.
	stale = paths.Socket(NewID())
	dead, err := net.Listen("unix", stale)
	if err != nil {
		t.Fatal(err)
	}
	dead.(*net.UnixListener).SetUnlinkOnClose(false)
	dead.Close()
	return live, stale
}

func fixByID(fixes []api.Fix, id string) (api.Fix, bool) {
	for _, f := range fixes {
		if f.ID == id {
			return f, true
		}
	}
	return api.Fix{}, false
}

func TestFixesFindAndApply(t *testing.T) {
	live, stale := fixHome(t)
	fixes := Fixes(api.FixArgs{})
	for _, id := range []string{"current-link", "sockets", "old-helpers"} {
		f, ok := fixByID(fixes, id)
		if !ok || f.Problem == "" || f.Action == "" || f.Command == "" || f.Done {
			t.Fatalf("%s: %+v (all: %+v)", id, f, fixes)
		}
	}
	if f, _ := fixByID(fixes, "old-helpers"); !strings.Contains(f.Command, "/v2") || strings.Contains(f.Command, "/v1") || strings.Contains(f.Command, "/v3") {
		t.Fatalf("old helpers: only v2 is old and unused: %s", f.Command)
	}
	if f, _ := fixByID(fixes, "sockets"); f.Command != "rm "+stale {
		t.Fatalf("sockets: %s", f.Command)
	}
	// Listing changed nothing.
	if _, err := os.Stat(stale); err != nil {
		t.Fatal("listing the fixes removed the stale socket")
	}

	done := Fixes(api.FixArgs{Apply: true, IDs: []string{"current-link", "sockets", "old-helpers"}})
	for _, f := range done {
		if f.ID != "terminfo" && f.ID != bwrapFixID && (!f.Done || f.Error != "") {
			t.Errorf("%s: %+v", f.ID, f)
		}
	}
	if t2, _ := os.Readlink(filepath.Join(paths.BinDir(), "current")); t2 != "v5" {
		t.Errorf("current points to %q", t2)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("the stale socket is still there")
	}
	if _, err := os.Stat(paths.Socket(live)); err != nil {
		t.Error("the live session's socket was removed")
	}
	for v, want := range map[string]bool{"v1": true, "v2": false, "v3": true, "v4": true, "v5": true} {
		if _, err := os.Stat(filepath.Join(paths.BinDir(), v)); (err == nil) != want {
			t.Errorf("%s kept = %v, want %v", v, err == nil, want)
		}
	}
	for _, f := range Fixes(api.FixArgs{}) {
		if f.ID != "terminfo" && f.ID != "linger" && f.ID != "restore-unit" {
			t.Errorf("still to fix after the fixes: %+v", f)
		}
	}
}

func TestFixesApplyOnlyWhatIsAsked(t *testing.T) {
	_, stale := fixHome(t)
	Fixes(api.FixArgs{Apply: true, IDs: []string{"current-link"}})
	if _, err := os.Stat(stale); err != nil {
		t.Fatal("a fix ran that nobody asked for")
	}
}

func TestTerminfoFix(t *testing.T) {
	if _, err := exec.LookPath("tic"); err != nil {
		t.Skip("no tic")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TERMINFO", filepath.Join(home, ".terminfo"))
	t.Setenv("TERMINFO_DIRS", "")
	if HasTerminfo("xterm-ghostty") {
		t.Skip("this system has xterm-ghostty")
	}
	f := terminfoFix("")
	if len(f) != 1 || !f[0].Manual || !strings.Contains(f[0].Command, "infocmp -x xterm-ghostty") {
		t.Fatalf("without a source the fix is manual: %+v", f)
	}
	f = terminfoFix("xterm-ghostty|a test entry,\n\tuse=xterm-256color,\n")
	if len(f) != 1 || f[0].Manual {
		t.Fatalf("with a source the doctor fixes it: %+v", f)
	}
	if err := f[0].run(); err != nil {
		t.Fatal(err)
	}
	if !HasTerminfo("xterm-ghostty") {
		t.Fatal("tic ran, but the entry is not there")
	}
}
