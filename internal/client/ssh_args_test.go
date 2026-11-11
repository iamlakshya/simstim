package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The options of decision 0034, as they must reach ssh.
var (
	wantNoForwarding = []string{"-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "PermitLocalCommand=no", "-o", "GSSAPIDelegateCredentials=no"}
	wantNoPorts      = []string{"-o", "ClearAllForwardings=yes"}
	wantOwn          = []string{"-o", "ControlMaster=no", "-o", "ControlPath=none"}
)

// Ask the real ssh whether it knows ForkAfterAuthentication now, before a
// test puts a fake ssh on PATH, which would answer for the whole binary.
func init() { _ = sshKnowsFork() }

// sshDirs gives the client a config folder and a cache folder of its own.
func sshDirs(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if runtime.GOOS == "windows" {
		t.Setenv("XDG_CACHE_HOME", t.TempDir())
	} else {
		t.Setenv("XDG_CACHE_HOME", shortDir(t))
	}
}

// hasRun reports whether args holds want, in order and side by side.
func hasRun(args, want []string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		if slices.Equal(args[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

// checkOptions checks that the options before the destination ("--") turn
// off forwarding, and clear port forwards when clearPorts is set.
func checkOptions(t *testing.T, name string, args []string, clearPorts bool) {
	t.Helper()
	end := slices.Index(args, "--")
	if end < 0 {
		t.Fatalf("%s: no -- in %q", name, args)
	}
	opts := args[:end]
	if !hasRun(opts, wantNoForwarding) {
		t.Errorf("%s: %q lacks %q before the destination", name, args, wantNoForwarding)
	}
	if got := hasRun(opts, wantNoPorts); got != clearPorts {
		t.Errorf("%s: ClearAllForwardings is %v in %q, want %v", name, got, args, clearPorts)
	}
	if hasRun(args[end:], wantNoForwarding) || hasRun(args[end:], wantNoPorts) {
		t.Errorf("%s: an option is after the destination, where ssh takes it as the remote command: %q", name, args)
	}
}

// sharedPath is the ControlPath option of a shared connection, or "".
func sharedPath(args []string) string {
	for i, a := range args {
		if i > 0 && args[i-1] == "-o" && strings.HasPrefix(a, "ControlPath=") {
			return strings.TrimPrefix(a, "ControlPath=")
		}
	}
	return ""
}

func TestEverySSHCommandTurnsOffForwarding(t *testing.T) {
	sshDirs(t)
	ctx := context.Background()
	for _, interactive := range []bool{false, true} {
		s := &SSH{Host: Host{Name: "box", SSH: "dev@box"}, Interactive: interactive}
		base, err := s.baseArgs()
		if err != nil {
			t.Fatal(err)
		}
		// ExitMaster and CheckMaster use baseArgs as they are.
		if !hasRun(base, wantNoForwarding) || hasRun(base, wantNoPorts) {
			t.Errorf("baseArgs = %q, want the forwarding options and no ClearAllForwardings", base)
		}
		cmd, err := s.Command(ctx, "true", "-T")
		if err != nil {
			t.Fatal(err)
		}
		checkOptions(t, "Command", cmd.Args[1:], true)
		cmd, err = s.Shell(ctx)
		if err != nil {
			t.Fatal(err)
		}
		checkOptions(t, "Shell", cmd.Args[1:], true)
	}
	git, err := GitSSHCommand(Host{Name: "box", SSH: "dev@box"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(git, " -o 'ForwardAgent=no' -o 'ForwardX11=no' -o 'PermitLocalCommand=no' -o 'GSSAPIDelegateCredentials=no' ") ||
		!strings.HasSuffix(git, " -o 'ClearAllForwardings=yes'") {
		t.Errorf("GIT_SSH_COMMAND = %q, want the forwarding options and ClearAllForwardings", git)
	}
}

// A master that an older client started may hold an agent that the user's
// config forwarded. The client's sockets have a name of their own, so it
// never uses one.
func TestSharedConnectionsHaveANewName(t *testing.T) {
	if !controlMaster {
		t.Skip("the client shares no SSH connections here")
	}
	sshDirs(t)
	args, err := (&SSH{Host: Host{Name: "box", SSH: "dev@box"}}).baseArgs()
	if err != nil {
		t.Fatal(err)
	}
	// The name is spelled out here: older clients used "%C", and a test
	// that read the constant would pass if it went back to that.
	p := sharedPath(args)
	if filepath.Base(p) != "nf-%C" || len(filepath.Dir(p))+1+len("nf-")+40 > maxSocketPath {
		t.Errorf("ControlPath = %q, want nf-%%C in a folder short enough for a socket", p)
	}
	if !hasRun(args, []string{"-o", "ControlMaster=auto"}) {
		t.Errorf("baseArgs = %q, want a shared connection", args)
	}
}

func TestWrapperStartsWithTheForwardingBlock(t *testing.T) {
	sshDirs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // the home folder on Windows
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("ForwardAgent yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(t.TempDir(), "repo-config")
	if err := os.WriteFile(own, []byte("ForwardAgent yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	block := "ForwardAgent no\nForwardX11 no\nPermitLocalCommand no\nGSSAPIDelegateCredentials no\nClearAllForwardings yes\n"
	wrapper, err := writeWrapper(Host{Name: "box", SSH: "box", SSHConfig: own})
	if err != nil {
		t.Fatal(err)
	}
	def, err := DefaultSSHConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{wrapper, def} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		// The block comes first, after the comment, before every Include.
		_, rest, _ := strings.Cut(string(b), "\n")
		if !strings.HasPrefix(rest, block) || !strings.Contains(rest, fmt.Sprintf("Include %q\n", filepath.Join(home, ".ssh", "config"))) {
			t.Errorf("%s =\n%s\nwant the forwarding block first, then the user's config", p, b)
		}
	}
	if b, _ := os.ReadFile(def); strings.Contains(string(b), own) || strings.Contains(string(b), "ServerAliveInterval") {
		t.Errorf("the default config holds a host's config or the defaults:\n%s", b)
	}
}

func TestSystemSSHConfig(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == "PROGRAMDATA" {
				return v
			}
			return ""
		}
	}
	for _, c := range []struct{ goos, programData, want string }{
		{"linux", `D:\Data`, "/etc/ssh/ssh_config"},
		{"darwin", "", "/etc/ssh/ssh_config"},
		{"windows", `D:\Data`, `D:\Data\ssh\ssh_config`},
		{"windows", `D:\Data\`, `D:\Data\ssh\ssh_config`},
		{"windows", "", `C:\ProgramData\ssh\ssh_config`},
	} {
		if got := systemSSHConfig(c.goos, env(c.programData)); got != c.want {
			t.Errorf("%s with PROGRAMDATA=%q: %q, want %q", c.goos, c.programData, got, c.want)
		}
	}
}

// The generated configs include the system's SSH config after the user's,
// and ForkAfterAuthentication no only where ssh knows the option.
func TestGeneratedConfigsIncludeTheSystemConfig(t *testing.T) {
	sshDirs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	sys := filepath.Join(t.TempDir(), "ssh_config")
	if err := os.WriteFile(sys, []byte("Host *\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldSys, oldFork := systemConfig, sshKnowsFork
	systemConfig = func() string { return sys }
	t.Cleanup(func() { systemConfig, sshKnowsFork = oldSys, oldFork })
	for _, knows := range []bool{false, true} {
		sshKnowsFork = func() bool { return knows }
		wrapper, err := writeWrapper(Host{Name: "box", SSH: "box"})
		if err != nil {
			t.Fatal(err)
		}
		def, err := DefaultSSHConfig()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{wrapper, def} {
			b, _ := os.ReadFile(p)
			if !strings.Contains(string(b), fmt.Sprintf("Include %q\n", sys)) {
				t.Errorf("%s does not include the system config:\n%s", p, b)
			}
			if got := strings.Contains(string(b), "\nForkAfterAuthentication no\n"); got != knows {
				t.Errorf("%s: ForkAfterAuthentication no is there: %v, want %v:\n%s", p, got, knows, b)
			}
			if i, j := strings.Index(string(b), "ForkAfterAuthentication"), strings.Index(string(b), "Include"); knows && i > j {
				t.Errorf("%s: the line comes after an Include", p)
			}
		}
	}
}

func TestForwardRisk(t *testing.T) {
	h := Host{Name: "box", SSH: "dev@box", SSHConfig: "~/infra/ssh_config"}
	if err := forwardRisk(h, map[string][]string{"localforward": {"[127.0.0.1]:5555 [127.0.0.1]:22"}, "tunnel": {"false"}}); err != nil {
		t.Errorf("a local forward and no tunnel: %v", err)
	}
	err := forwardRisk(h, map[string][]string{"remoteforward": {"6666 [127.0.0.1]:22"}, "tunnel": {"point-to-point"}})
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "RemoteForward 6666 [127.0.0.1]:22, Tunnel point-to-point") ||
		!strings.Contains(ue.Hint, "Remove those lines from the block for dev@box in ~/infra/ssh_config or ~/.ssh/config") {
		t.Errorf("a remote forward and a tunnel: %v", err)
	}
	if err := forwardRisk(h, map[string][]string{"forkafterauthentication": {"no"}}); err != nil {
		t.Errorf("no fork: %v", err)
	}
	err = forwardRisk(h, map[string][]string{"forkafterauthentication": {"yes"}})
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "ForkAfterAuthentication yes") || !strings.Contains(ue.Hint, "Remove that line") {
		t.Errorf("a fork after authentication: %v", err)
	}
}

// hangingSSH puts an ssh on PATH that never ends by itself, and whose child
// holds stdout and stderr open after ssh is killed, as a Match exec or a
// ProxyCommand can. The child makes the file that it returns once it runs.
func hangingSSH(t *testing.T) (ready string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake ssh is a POSIX shell script")
	}
	sshDirs(t)
	dir := t.TempDir()
	ready = filepath.Join(dir, "ready")
	script := "#!/bin/sh\n(touch '" + ready + "'; exec sleep 15) &\nexec sleep 15\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return ready
}

// cancelWhenReady cancels once the file ready exists, or after 10 s, and
// sends the time when it cancelled.
func cancelWhenReady(ready string, cancel context.CancelFunc) <-chan time.Time {
	done := make(chan time.Time, 1)
	go func() {
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if _, err := os.Stat(ready); err == nil {
				break
			}
		}
		cancel()
		done <- time.Now()
	}()
	return done
}

// The ssh -G of forward's check and of the debug bundle returns soon after
// it is cancelled, even when a child of ssh keeps its output open.
func TestSSHGReturnsWhenCancelled(t *testing.T) {
	ready := hangingSSH(t)
	s := &SSH{Host: Host{Name: "box", SSH: "box"}, Interactive: true}
	for name, run := range map[string]func(context.Context) error{
		"forward": func(ctx context.Context) error {
			_, err := s.Forward(ctx, []ForwardPair{{Local: 45678, RemoteHost: "127.0.0.1", Remote: 3000}})
			return err
		},
		"ConfigValues": func(ctx context.Context) error { _, err := s.ConfigValues(ctx); return err },
	} {
		_ = os.Remove(ready)
		ctx, cancel := context.WithCancel(context.Background())
		cancelled := cancelWhenReady(ready, cancel)
		err := run(ctx)
		returned := time.Now()
		cancel()
		if _, statErr := os.Stat(ready); statErr != nil {
			t.Fatalf("%s: the fake ssh's child never ran", name)
		}
		// sshWaitDelay is 2 s, and the child holds the output for 15 s.
		if took := returned.Sub(<-cancelled); err == nil || took > 8*time.Second {
			t.Errorf("%s returned %v %v after it was cancelled, want an error within a few seconds", name, err, took)
		}
	}
}

// A security check must read every line of ssh -G, or fail.
func TestParseConfigValuesReadsEveryLine(t *testing.T) {
	long := "xauthlocation /" + strings.Repeat("x", 70000) + "\nremoteforward 6666 [127.0.0.1]:22\n"
	vals, err := parseConfigValues([]byte(long))
	if err != nil || !slices.Equal(vals["remoteforward"], []string{"6666 [127.0.0.1]:22"}) {
		t.Errorf("a line of 70,000 characters: %v, %v, want the line after it", err, vals["remoteforward"])
	}
	tooLong := "user dev\nxauthlocation /" + strings.Repeat("x", maxConfigLine) + "\nremoteforward 6666 [127.0.0.1]:22\n"
	vals, err = parseConfigValues([]byte(tooLong))
	if err == nil {
		t.Errorf("a line over %d bytes: no error, values %v", maxConfigLine, vals["remoteforward"])
	}
	if !slices.Equal(vals["user"], []string{"dev"}) {
		t.Errorf("the values before the long line are lost: %v", vals)
	}
}

// forward refuses to start when it cannot read all of what ssh -G prints.
func TestForwardRefusesAnAnswerItCannotRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake ssh is a POSIX shell script")
	}
	sshDirs(t)
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'xauthlocation /'\nhead -c " + strconv.Itoa(maxConfigLine+10) + " /dev/zero | tr '\\0' x\n" +
		"printf '\\nremoteforward 6666 [127.0.0.1]:22\\n'\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := &SSH{Host: Host{Name: "box", SSH: "box"}, Interactive: true}
	_, err := s.Forward(context.Background(), []ForwardPair{{Local: 45678, RemoteHost: "127.0.0.1", Remote: 3000}})
	var ue *UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "cannot read all of what ssh -G prints") || !strings.Contains(ue.Hint, "ssh -G box") {
		t.Errorf("err = %.300v, want a refusal", err)
	}
}

func TestNoForwardingIsAFreshCopy(t *testing.T) {
	a := NoForwarding()
	a[1] = "ForwardAgent=yes"
	if b := NoForwarding(); !slices.Equal(b, wantNoForwarding) {
		t.Fatalf("a change to one result reached the next: %q", b)
	}
	c := NoPortForwards()
	c[1] = "ClearAllForwardings=no"
	if d := NoPortForwards(); !slices.Equal(d, wantNoPorts) {
		t.Fatalf("a change to one result reached the next: %q", d)
	}
}

// loggingSSH puts an ssh on PATH that writes its arguments to a file, one
// command to a line, then runs the remote command here. It writes a line to
// the log that -E names, and does nothing for -O and -G.
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
		if name != "VerboseLog" && name != "ConfigValues" && filepath.Base(sharedPath(args)) != "nf-%C" {
			t.Errorf("%s: %q, want the shared connection's new name", name, args)
		}
	}
}

// With no private folder for the shared connections, every command has a
// connection of its own, so a ControlPath in the user's config never names
// the master, and the client stops no master and asks about none.
func TestNoControlFolderMeansAConnectionOfItsOwn(t *testing.T) {
	logPath := loggingSSH(t)
	t.Setenv("XDG_CACHE_HOME", "/dev/null") // no folder can be made in it
	if d := controlDir(); d != "" {
		t.Fatalf("controlDir = %q, want none", d)
	}
	ctx := context.Background()
	s := &SSH{Host: Host{Name: "box", SSH: "box"}}
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
