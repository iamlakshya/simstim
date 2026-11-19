package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The tests in this file run the real ssh with -G, which prints the values
// that ssh would use and connects to nothing.

// realSSH skips the test where the real ssh cannot run, and gives the client
// folders and a home folder of their own, so that no ~/.ssh/config of the
// person who runs the tests is read. It returns the home folder's .ssh.
func realSSH(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the client shares no SSH connections on Windows, and its ssh may be another")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}
	sshDirs(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// writeFile writes body to a new file in a new folder, and returns its path.
func writeFile(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// sshValues runs the real `ssh -G` with the options that args gives before
// the destination.
func sshValues(t *testing.T, args []string) map[string][]string {
	t.Helper()
	opts, dest := args, "box.invalid"
	if i := slices.Index(args, "--"); i >= 0 {
		opts = args[:i]
		if i+1 < len(args) && strings.HasSuffix(args[i+1], ".invalid") {
			dest = args[i+1]
		}
	}
	cmd := exec.CommandContext(context.Background(), "ssh", slices.Concat(opts, []string{"-G", "--", dest})...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ssh -G: %v", err)
	}
	vals, err := parseConfigValues(out)
	if err != nil {
		t.Fatal(err)
	}
	return vals
}

// checkOff checks that ssh would forward neither the agent, nor X11, nor
// Kerberos credentials, and run no LocalCommand.
func checkOff(t *testing.T, name string, vals map[string][]string) {
	t.Helper()
	for _, k := range []string{"forwardagent", "forwardx11", "permitlocalcommand"} {
		if got := vals[k]; !slices.Equal(got, []string{"no"}) {
			t.Errorf("%s: %s is %q, want no", name, k, got)
		}
	}
	// An ssh built without GSSAPI prints no such line, and delegates nothing.
	if got, ok := vals["gssapidelegatecredentials"]; ok && !slices.Equal(got, []string{"no"}) {
		t.Errorf("%s: gssapidelegatecredentials is %q, want no", name, got)
	}
}

// everything turns on every kind of forwarding that a config can ask for.
const everything = "ForwardAgent yes\nForwardX11 yes\nPermitLocalCommand yes\nLocalCommand true\n" +
	"GSSAPIAuthentication yes\nGSSAPIDelegateCredentials yes\n" +
	"LocalForward 127.0.0.1:5555 127.0.0.1:22\nRemoteForward 6666 127.0.0.1:22\nDynamicForward 7777\nTunnel yes\n"

// TestOptionsWinOverTheConfig gives the real ssh a config that turns every
// kind of forwarding on, and checks that the client's options still turn
// it off (decision 0034).
func TestOptionsWinOverTheConfig(t *testing.T) {
	userSSH := realSSH(t)
	writeFile(t, filepath.Join(userSSH, "config"), everything)
	cfg := writeFile(t, filepath.Join(t.TempDir(), "config"), everything)
	ctx := context.Background()
	s := &SSH{Host: Host{Name: "box", SSH: "box.invalid", SSHConfig: cfg}}

	cmd, err := s.Command(ctx, "true", "-T")
	if err != nil {
		t.Fatal(err)
	}
	cfgVals, err := s.ConfigValues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for name, vals := range map[string]map[string][]string{
		"the bridge":   sshValues(t, cmd.Args[1:]),
		"ConfigValues": cfgVals,
	} {
		checkOff(t, name, vals)
		for _, k := range []string{"localforward", "remoteforward", "dynamicforward"} {
			if got := vals[k]; len(got) > 0 {
				t.Errorf("%s: %s is %q, want none", name, k, got)
			}
		}
		if got := vals["tunnel"]; !slices.Equal(got, []string{"false"}) {
			t.Errorf("%s: tunnel is %q, want false", name, got)
		}
	}
}

// A ClearAllForwardings yes in the config does not clear forward's ports.
func TestForwardKeepsItsPorts(t *testing.T) {
	userSSH := realSSH(t)
	writeFile(t, filepath.Join(userSSH, "config"), "ClearAllForwardings yes\nForwardAgent yes\nForwardX11 yes\nPermitLocalCommand yes\nLocalCommand true\n")
	s := &SSH{Host: Host{Name: "box", SSH: "box.invalid"}, Interactive: true}
	cmd, err := s.Forward(context.Background(), []ForwardPair{{Local: 45678, RemoteHost: "127.0.0.1", Remote: 3000}})
	if err != nil {
		t.Fatal(err)
	}
	vals := sshValues(t, cmd.Args[1:])
	checkOff(t, "forward", vals)
	if !slices.Contains(vals["localforward"], "[127.0.0.1]:45678 [127.0.0.1]:3000") {
		t.Errorf("forward lost its own port: localforward is %q", vals["localforward"])
	}
	if got := vals["controlmaster"]; !slices.Equal(got, []string{"false"}) {
		t.Errorf("forward: controlmaster is %q, want false", got)
	}
}

// forward refuses a config that would let the host reach this computer.
func TestForwardRefusesAWayIn(t *testing.T) {
	userSSH := realSSH(t)
	// ssh -G prints xauthlocation before remoteforward. A line longer than
	// bufio.Scanner's default of 64 KiB once hid every line after it.
	long := "XAuthLocation /" + strings.Repeat("x", 70000) + "\n    RemoteForward 6666 127.0.0.1:22"
	for line, want := range map[string]string{
		"RemoteForward 6666 127.0.0.1:22": "RemoteForward 6666 [127.0.0.1]:22",
		"Tunnel yes":                      "Tunnel point-to-point",
		long:                              "RemoteForward 6666 [127.0.0.1]:22",
	} {
		writeFile(t, filepath.Join(userSSH, "config"), "Host box.invalid\n    "+line+"\n")
		s := &SSH{Host: Host{Name: "box", SSH: "box.invalid"}, Interactive: true}
		_, err := s.Forward(context.Background(), []ForwardPair{{Local: 45678, RemoteHost: "127.0.0.1", Remote: 3000}})
		var ue *UserError
		if !errors.As(err, &ue) || !strings.Contains(ue.Msg, want) || !strings.Contains(ue.Hint, "Remove that line from the block for box.invalid") {
			t.Errorf("%.40s: err = %.300v, want a refusal that names %q", line, err, want)
		}
	}
}

// With no private folder for the shared connections, the real ssh never
// takes the ControlPath of the user's config.
func TestNoControlFolderIgnoresTheUsersControlPath(t *testing.T) {
	userSSH := realSSH(t)
	t.Setenv("XDG_CACHE_HOME", "/dev/null") // no folder can be made in it
	writeFile(t, filepath.Join(userSSH, "config"), "ControlMaster auto\nControlPath /tmp/user-master-%C\nControlPersist 600\n")
	cmd, err := (&SSH{Host: Host{Name: "box", SSH: "box.invalid"}}).Command(context.Background(), "true", "-T")
	if err != nil {
		t.Fatal(err)
	}
	vals := sshValues(t, cmd.Args[1:])
	// ssh -G prints no controlpath line for "none".
	if len(vals["controlpath"]) > 0 || !slices.Equal(vals["controlmaster"], []string{"false"}) {
		t.Errorf("controlpath %q, controlmaster %q, want none and false", vals["controlpath"], vals["controlmaster"])
	}
}

// Where ssh knows ForkAfterAuthentication, the generated config turns it
// off for the host, for forward and for a ProxyJump hop, and the shared
// master still stays for its 10 minutes.
func TestNothingForksAfterAuthentication(t *testing.T) {
	userSSH := realSSH(t)
	if !sshKnowsFork() {
		t.Skip("OpenSSH before 8.7 has no ForkAfterAuthentication")
	}
	writeFile(t, filepath.Join(userSSH, "config"), "ForkAfterAuthentication yes\nHost jump.invalid\n    ForkAfterAuthentication yes\n")
	s := &SSH{Host: Host{Name: "box", SSH: "box.invalid"}, Interactive: true}
	fwd, err := s.Forward(context.Background(), []ForwardPair{{Local: 45678, RemoteHost: "127.0.0.1", Remote: 3000}})
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	bridge, err := s.Command(context.Background(), "true", "-T")
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"forward":    fwd.Args[1:],
		"the bridge": bridge.Args[1:],
		// The command that ssh runs for a hop: ssh -F <wrapper> -W '[%h]:%p' <jump>.
		"the hop": {"-F", wrapperPath(s.Host), "-W", "[box.invalid]:22", "--", "jump.invalid"},
	} {
		if got := sshValues(t, args)["forkafterauthentication"]; !slices.Equal(got, []string{"no"}) {
			t.Errorf("%s: forkafterauthentication is %q, want no", name, got)
		}
	}
	if !controlMaster || sharedDir() == "" {
		return
	}
	if got := sshValues(t, bridge.Args[1:])["controlpersist"]; !slices.Equal(got, []string{"600"}) {
		t.Errorf("the bridge: controlpersist is %q, want 600", got)
	}
}

// The generated configs include the system's SSH config, which ssh reads by
// itself only without -F, so its settings still apply.
func TestTheSystemConfigStillApplies(t *testing.T) {
	realSSH(t)
	sys := writeFile(t, filepath.Join(t.TempDir(), "ssh_config"), "Host box.invalid\n    HostName 192.0.2.9\n")
	old := systemConfig
	systemConfig = func() string { return sys }
	t.Cleanup(func() { systemConfig = old })
	wrapper, err := writeWrapper(Host{Name: "box", SSH: "box.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	def, err := DefaultSSHConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []string{wrapper, def} {
		if got := sshValues(t, []string{"-F", cfg})["hostname"]; !slices.Equal(got, []string{"192.0.2.9"}) {
			t.Errorf("%s: hostname is %q, want the system config's 192.0.2.9", filepath.Base(cfg), got)
		}
	}
}

// A ProxyJump hop reads the generated config but not the client's command
// line. The config's first lines clear the forwards and the tunnel of the
// user's config for it, even when that config says ClearAllForwardings no.
func TestProxyJumpHopClearsForwards(t *testing.T) {
	userSSH := realSSH(t)
	writeFile(t, filepath.Join(userSSH, "config"), "ClearAllForwardings no\nRemoteForward 6666 127.0.0.1:22\nTunnel yes\n"+
		"LocalForward 127.0.0.1:5555 127.0.0.1:22\nDynamicForward 7777\n")
	wrapper, err := writeWrapper(Host{Name: "box", SSH: "box.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	// The command that ssh runs for the hop: ssh -F <wrapper> -W '[%h]:%p' <jump>.
	vals := sshValues(t, []string{"-F", wrapper, "-W", "[box.invalid]:22"})
	for _, k := range []string{"remoteforward", "localforward", "dynamicforward"} {
		if got := vals[k]; len(got) > 0 {
			t.Errorf("the hop: %s is %q, want none", k, got)
		}
	}
	if !slices.Equal(vals["tunnel"], []string{"false"}) || !slices.Equal(vals["clearallforwardings"], []string{"yes"}) {
		t.Errorf("the hop: tunnel %q, clearallforwardings %q, want false and yes", vals["tunnel"], vals["clearallforwardings"])
	}
}

// A ProxyJump hop is a second ssh that ssh starts with the same -F, but
// without the client's -o options. The generated config's first lines turn
// forwarding off for it too.
func TestProxyJumpHopIsHardened(t *testing.T) {
	userSSH := realSSH(t)
	writeFile(t, filepath.Join(userSSH, "config"), everything)
	s := &SSH{Host: Host{Name: "box", SSH: "box.invalid"}}
	cmd, err := s.Command(context.Background(), "true", "-T")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := wrapperPath(s.Host)
	opts := cmd.Args[1:slices.Index(cmd.Args, "--")]
	var stderr bytes.Buffer
	g := exec.CommandContext(context.Background(), "ssh", slices.Concat(opts, []string{"-vv", "-G", "-J", "jump.invalid", "--", "box.invalid"})...)
	g.Stderr = &stderr
	if err := g.Run(); err != nil {
		t.Fatalf("ssh -G -J: %v\n%s", err, stderr.String())
	}
	_, hop, found := strings.Cut(stderr.String(), "Setting implicit ProxyCommand from ProxyJump: ")
	hop, _, _ = strings.Cut(hop, "\n")
	switch {
	case !found:
		t.Log("this ssh does not print the hop's command; the hop's config is checked below")
	case !strings.Contains(hop, "-F "+wrapper+" ") || strings.Contains(hop, "ForwardAgent=no"):
		t.Errorf("the hop runs %q, want -F %s and none of the client's -o options", hop, wrapper)
	}
	// The hop reads only the generated config: forwarding is off there too.
	checkOff(t, "the hop", sshValues(t, []string{"-F", wrapper}))
}
