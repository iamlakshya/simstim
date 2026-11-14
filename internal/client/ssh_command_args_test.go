package client

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func loggingSSH(t *testing.T) (logPath string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake ssh is a POSIX shell script")
	}
	sshDirs(t)
	dir := t.TempDir()
	logPath = filepath.Join(dir, "args.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
prev=
for a; do
	if [ "$prev" = -E ]; then echo "debug1: fake" > "$a"; fi
	prev=$a; last=$a
done
case " $* " in *" -O "*|*" -G "*) exit 0;; esac
exec sh -c "$last"
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// loggedArgs returns each command line that the fake ssh ran.
func loggedArgs(t *testing.T, logPath string) [][]string {
	t.Helper()
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		out = append(out, strings.Fields(line))
	}
	return out
}

// TestRunCommandsTurnOffForwarding runs each SSH method that starts ssh
// itself through a fake ssh, and reads the arguments that ssh got.
func TestRunCommandsTurnOffForwarding(t *testing.T) {
	logPath := loggingSSH(t)
	ctx := context.Background()
	s := &SSH{Host: Host{Name: "box", SSH: "box"}}
	pinFor(t, "box")

	p, err := s.Start(ctx, "true") // the bridge
	if err != nil {
		t.Fatal(err)
	}
	_ = p.Stdin().Close()
	if err := p.Wait(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := s.Run(ctx, "true", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := s.VerboseLog(ctx); err != nil {
		t.Fatalf("VerboseLog: %v", err)
	}
	if _, err := s.ConfigValues(ctx); err != nil {
		t.Fatalf("ConfigValues: %v", err)
	}
	s.ExitMaster(ctx)
	_, _ = s.CheckMaster(ctx)

	runs := loggedArgs(t, logPath)
	// The fake ssh runs only where the client shares connections, so
	// ExitMaster runs too.
	names := []string{"Start", "Run", "VerboseLog", "ConfigValues", "ExitMaster", "CheckMaster"}
	if len(runs) != len(names) {
		t.Fatalf("ssh ran %d times, want %d: %q", len(runs), len(names), runs)
	}
	for i, name := range names {
		args := runs[i]
		switch name {
		case "ExitMaster", "CheckMaster":
			// A control command opens no session, so it needs no
			// ClearAllForwardings, and it has no "--".
			if !hasRun(args, wantNoForwarding) || hasRun(args, wantNoPorts) {
				t.Errorf("%s: %q, want %q and no %q", name, args, wantNoForwarding, wantNoPorts)
			}
		case "VerboseLog":
			checkOptions(t, name, args, true)
			if !hasRun(args, wantOwn) {
				t.Errorf("%s: %q, want a connection of its own", name, args)
			}
		default:
			checkOptions(t, name, args, true)
		}
		if name != "VerboseLog" && name != "ConfigValues" && !newSocketName.MatchString(filepath.Base(sharedPath(args))) {
			t.Errorf("%s: %q, want the shared connection's new name", name, args)
		}
	}
}

// With no private folder for the shared connections, every command has a
// connection of its own, so a ControlPath in the user's config never names
// the master, and the client stops no master and asks about none.
func TestNoControlFolderMeansAConnectionOfItsOwn(t *testing.T) {
	logPath := loggingSSH(t)
	oldFallback := controlFallbackDir
	controlFallbackDir = func(uint32) string { return "/dev/null" }
	defer func() { controlFallbackDir = oldFallback }()
	t.Setenv("XDG_CACHE_HOME", "/dev/null") // no folder can be made in it
	if d := controlDir(); d != "" {
		t.Fatalf("controlDir = %q, want none", d)
	}
	ctx := context.Background()
	s := &SSH{Host: Host{Name: "box", SSH: "box"}}
	pinFor(t, "box")
	for _, mk := range []func() (*exec.Cmd, error){
		func() (*exec.Cmd, error) { return s.Command(ctx, "true", "-T") },
		func() (*exec.Cmd, error) { return s.Shell(ctx) },
	} {
		cmd, err := mk()
		if err != nil {
			t.Fatal(err)
		}
		if !hasRun(cmd.Args, wantOwn) || hasRun(cmd.Args, []string{"-o", "ControlMaster=auto"}) {
			t.Errorf("%q, want a connection of its own", cmd.Args)
		}
	}
	s.ExitMaster(ctx)
	if running, _ := s.CheckMaster(ctx); running {
		t.Error("CheckMaster found a master")
	}
	if b, _ := os.ReadFile(logPath); len(b) > 0 {
		t.Errorf("ExitMaster or CheckMaster ran ssh: %s", b)
	}
}

// Forward has a connection of its own, keeps its -L options whatever the
// config says, and checks the config with ssh -G before it starts.
func TestForwardHasAConnectionOfItsOwn(t *testing.T) {
	logPath := loggingSSH(t)
	s := &SSH{Host: Host{Name: "box", SSH: "box"}, Interactive: true}
	pinFor(t, "box")
	cmd, err := s.Forward(context.Background(), []ForwardPair{{Local: 45678, RemoteHost: "127.0.0.1", Remote: 3000}})
	if err != nil {
		t.Fatal(err)
	}
	args := cmd.Args[1:]
	// Forward keeps its own -L options, which ClearAllForwardings=yes would clear.
	checkOptions(t, "Forward", args, false)
	for _, want := range [][]string{wantOwn, {"-o", "ClearAllForwardings=no"}, {"-L", "127.0.0.1:45678:127.0.0.1:3000"}} {
		if !hasRun(args, want) {
			t.Errorf("Forward = %q, want %q", args, want)
		}
	}
	if hasRun(args, []string{"-o", "ControlMaster=auto"}) || sharedPath(args) != "none" {
		t.Errorf("Forward = %q uses the shared connection", args)
	}
	runs := loggedArgs(t, logPath)
	if len(runs) != 1 || !slices.Contains(runs[0], "-G") || !slices.Equal(runs[0][:slices.Index(runs[0], "-G")], args[:slices.Index(args, "--")]) {
		t.Errorf("before it starts, ssh -G ran with %q, want Forward's own options %q", runs, args)
	}
}

func expandedTestSocket(t *testing.T, args []string) string {
	t.Helper()
	path := sharedPath(args)
	if path == "" || path == "none" {
		t.Fatalf("no shared socket in %q", args)
	}
	return strings.ReplaceAll(strings.Trim(path, `"`), "%C", strings.Repeat("1", 40))
}
