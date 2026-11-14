//go:build !windows

package client

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func controlSSHFixture(t *testing.T, script string) string {
	t.Helper()
	sshDirs(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"HOME": dir, "USERPROFILE": dir, "XDG_CONFIG_HOME": filepath.Join(dir, "config"),
		"XDG_STATE_HOME": filepath.Join(dir, "state"), "XDG_DATA_HOME": filepath.Join(dir, "data"),
		"XDG_RUNTIME_DIR": filepath.Join(dir, "run"), "XDG_CACHE_HOME": shortDir(t),
		"GNUPGHOME": filepath.Join(dir, "gnupg"), "GIT_CONFIG_GLOBAL": filepath.Join(dir, "gitconfig"),
	} {
		t.Setenv(name, value)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	pinFor(t, "box")
	return dir
}

func TestMasterControlHasStableDiagnostics(t *testing.T) {
	dir := controlSSHFixture(t, `
if [ "$LC_ALL" != C ] || [ "$CONTROL_KEEP_ENV" != kept ]; then
  echo 'Control socket: Datei nicht gefunden' >&2
  exit 255
fi
case " $* " in *" LogLevel=ERROR "*) ;; *) exit 255;; esac
case " $* " in
  *" -O exit "*)
    if [ -f "$CONTROL_STATE" ]; then rm -f "$CONTROL_STATE"; exit 0; fi
    ;;
  *" -O check "*)
    if [ -f "$CONTROL_STATE" ]; then echo 'Master running (pid=4242)' >&2; exit 0; fi
    ;;
esac
echo 'Control socket connect(fixture): No such file or directory' >&2
exit 255
`)
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	t.Setenv("CONTROL_KEEP_ENV", "kept")
	state := filepath.Join(dir, "master.state")
	t.Setenv("CONTROL_STATE", state)
	config := filepath.Join(dir, "user.ssh")
	if err := os.WriteFile(config, []byte("Host *\n    LogLevel QUIET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &SSH{Host: Host{Name: "box", SSH: "box", SSHConfig: config}}
	if err := s.RetireMaster(context.Background()); err != nil {
		t.Errorf("absent master with quiet/non-English config: %v", err)
	}
	if err := os.WriteFile(state, []byte("running"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireMaster(context.Background()); err != nil {
		t.Errorf("active master with quiet/non-English config: %v", err)
	}
	if err := os.WriteFile(state, []byte("running"), 0o600); err != nil {
		t.Fatal(err)
	}
	if running, pid := s.CheckMaster(context.Background()); !running || pid != 4242 {
		t.Errorf("check master = %v, %d", running, pid)
	}
}

func TestMasterControlTargetBoundaryAndHint(t *testing.T) {
	dir := controlSSHFixture(t, `
printf '%s\n' "$@" > "$CONTROL_ARGS"
echo 'Control socket connect(fixture): Operation not permitted' >&2
exit 255
`)
	argsPath := filepath.Join(dir, "args")
	t.Setenv("CONTROL_ARGS", argsPath)
	marker := filepath.Join(dir, "injected")
	target := "-oProxyCommand=none; touch " + marker + "; echo 'quoted'"
	s := &SSH{Host: Host{Name: "box", SSH: target}}
	checkArgs := func() {
		t.Helper()
		b, err := os.ReadFile(argsPath)
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
		if len(args) < 2 || args[len(args)-2] != "--" || args[len(args)-1] != target {
			t.Errorf("SSH target is not protected by --: %q", args)
		}
	}
	err := s.RetireMaster(context.Background())
	checkArgs()
	var ue *UserError
	if !errors.As(err, &ue) || ue.Code != "reconnect_failed" {
		t.Fatalf("retire error = %#v, %v", ue, err)
	}
	if !strings.HasPrefix(ue.Hint, "ssh ") || !strings.Contains(ue.Hint, "ControlPath=") {
		t.Fatalf("hint does not target the actual control socket: %q", ue.Hint)
	}
	// Execute the displayed command against only the fake ssh. The hostile
	// target must remain one literal argument, never a second shell command.
	_ = exec.Command("/bin/sh", "-c", ue.Hint).Run()
	checkArgs()
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the hint ran part of its target: %v", err)
	}
	s.CheckMaster(context.Background())
	checkArgs()
}

func TestControlSocketFixUsesTheFallbackDirectory(t *testing.T) {
	dir := controlSSHFixture(t, "exit 1\n")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, strings.Repeat("x", 90)))
	cm := controlDir()
	if cm == "" || cm == filepath.Join(CacheDir(), "cm") {
		t.Fatalf("no fallback control directory: %q", cm)
	}
	deadPath := indexedControlFixture(t, Host{Name: "box", SSH: "box"}, strings.Repeat("k", 26))
	livePath := filepath.Join(filepath.Dir(deadPath), "nf-"+strings.Repeat("b", 40))
	live, err := net.ListenUnix("unix", &net.UnixAddr{Name: livePath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = live.Close() })
	dead, err := net.ListenUnix("unix", &net.UnixAddr{Name: deadPath, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	dead.SetUnlinkOnClose(false)
	_ = dead.Close()
	t.Cleanup(func() { _ = os.Remove(deadPath) })
	fixes := controlSocketFix(context.Background())
	if len(fixes) != 1 || !slices.Contains(strings.Fields(fixes[0].Command), deadPath) || strings.Contains(fixes[0].Command, livePath) {
		t.Fatalf("fallback fixes = %+v", fixes)
	}
	if err := fixes[0].run(context.Background(), &fixes[0].Fix); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(deadPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale fallback socket remains: %v", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Errorf("live fallback socket removed: %v", err)
	}
}
