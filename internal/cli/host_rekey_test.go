package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// startMiniSSH runs a minimal in-process SSH server for CLI host-key tests.
func startMiniSSH(t *testing.T, sig ssh.Signer) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		cfg := &ssh.ServerConfig{
			PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
				return nil, errors.New("no auth")
			},
		}
		cfg.AddHostKey(sig)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc, _, _, err := ssh.NewServerConn(c, cfg)
				if err == nil {
					_ = sc.Close()
				}
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func makeSigner(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, ssh.FingerprintSHA256(s.PublicKey())
}

func TestHostRekeyCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real OpenSSH test requires POSIX ssh")
	}
	hosttest.New(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}

	sig1, fp1 := makeSigner(t)
	sig2, fp2 := makeSigner(t)
	port := startMiniSSH(t, sig1)

	sshCfg := filepath.Join(t.TempDir(), "ssh.config")
	cfgBody := fmt.Sprintf("Host rekeybox\n  HostName 127.0.0.1\n  Port %d\n  User testuser\n  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n", port)
	if err := os.WriteFile(sshCfg, []byte(cfgBody), 0o600); err != nil {
		t.Fatal(err)
	}

	h := client.Host{Name: "box", SSH: "rekeybox", SSHConfig: sshCfg}
	ctx := context.Background()
	if err := client.SaveHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PinHostKey(ctx, h, client.PinOptions{HostKey: fp1}); err != nil {
		t.Fatalf("initial PinHostKey: %v", err)
	}

	// Unknown host -> exit 3.
	if got, code := lineErr(t, "host", "rekey", "nope"); code != 3 || !strings.Contains(got, `no host named "nope"`) {
		t.Errorf("rekey unknown host: exit %d\n%s", code, got)
	}

	// Non-SSH host -> needs_ssh.
	if err := client.SaveHost(ctx, client.Host{Name: "dock", Machine: &client.Machine{Provider: "docker", ID: "d1"}}); err != nil {
		t.Fatal(err)
	}
	fails(t, "no SSH host key", "host", "rekey", "dock")

	// --confirm mismatch -> exit 2.
	if got, code := lineErr(t, "host", "rekey", "box", "--confirm", "other"); code != 2 || !strings.Contains(got, "does not match") {
		t.Errorf("rekey --confirm mismatch: exit %d\n%s", code, got)
	}

	// Start a second server with sig2 and point sshCfg to it (simulating host reinstall).
	port2 := startMiniSSH(t, sig2)
	cfgBody2 := fmt.Sprintf("Host rekeybox\n  HostName 127.0.0.1\n  Port %d\n  User testuser\n  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n", port2)
	if err := os.WriteFile(sshCfg, []byte(cfgBody2), 0o600); err != nil {
		t.Fatal(err)
	}

	// With input off and no --confirm -> exit 4, hint names --confirm box, old pin stays.
	if got, code := lineErr(t, "host", "rekey", "box", "--no-input"); code != 4 || !strings.Contains(got, "--confirm box") {
		t.Errorf("rekey --no-input: exit %d\n%s", code, got)
	}
	if fps, err := client.PinnedFingerprints("box"); err != nil || fps[0] != fp1 {
		t.Fatalf("pin changed on --no-input: %v, %v", fps, err)
	}

	// With --host-key mismatch -> exit 5, old pin stays.
	if got, code := lineErr(t, "host", "rekey", "box", "--host-key", fp1, "--confirm", "box"); code != 5 || !strings.Contains(got, "did not show host key") {
		t.Errorf("rekey --host-key mismatch: exit %d\n%s", code, got)
	}

	// Interactive prompt: Enter keeps the old pin.
	var out bytes.Buffer
	if err := rekeyHost(ctx, testPrompts("\n", &out), "box", "", ""); err != nil {
		t.Fatalf("rekey Enter: %v", err)
	}
	if !strings.Contains(out.String(), "Old host key: "+fp1) ||
		!strings.Contains(out.String(), "New host key: "+fp2) ||
		!strings.Contains(out.String(), "Kept the pinned host key of box.") {
		t.Errorf("rekey Enter output:\n%s", out.String())
	}
	if fps, _ := client.PinnedFingerprints("box"); fps[0] != fp1 {
		t.Fatalf("Enter replaced the pin: %v", fps)
	}

	// Interactive prompt: wrong typed name cancels and keeps old pin.
	out.Reset()
	if err := rekeyHost(ctx, testPrompts("wrong\n", &out), "box", "", ""); !isCancel(err) {
		t.Fatalf("rekey wrong name = %v, want cancel", err)
	}
	if fps, _ := client.PinnedFingerprints("box"); fps[0] != fp1 {
		t.Fatalf("wrong name replaced the pin: %v", fps)
	}

	// Interactive prompt: exact typed name replaces the pin.
	out.Reset()
	if err := rekeyHost(ctx, testPrompts("box\n", &out), "box", "", ""); err != nil {
		t.Fatalf("rekey exact name: %v", err)
	}
	if !strings.Contains(out.String(), "Replaced the pinned host key of box: "+fp2) {
		t.Errorf("rekey output:\n%s", out.String())
	}
	if fps, err := client.PinnedFingerprints("box"); err != nil || fps[0] != fp2 {
		t.Fatalf("pin after interactive rekey = %v, %v; want %s", fps, err, fp2)
	}

	// Non-interactive --confirm + --host-key succeeds cleanly.
	_, errOut, code := mainRun(t, "host", "rekey", "box", "--host-key", fp2, "--confirm", "box", "--no-input")
	if code != 0 || !strings.Contains(errOut, "Replaced the pinned host key of box: "+fp2) {
		t.Fatalf("rekey --confirm --host-key: exit %d\n%s", code, errOut)
	}

	// host rm removes the pin file too.
	outRm := okSaid(t, "host", "rm", "box")
	if !strings.Contains(outRm, "Forgot box.") {
		t.Errorf("host rm = %q", outRm)
	}
	if client.HasPin("box") {
		t.Error("host rm left pinned host key file behind")
	}

	// host add with --no-input on an unknown host fails with exit code 4 and leaves no pin.
	_, errOut, code = mainRun(t, "host", "add", "fresh", "rekeybox", "--ssh-config", sshCfg, "--no-input")
	if code != 4 || !strings.Contains(errOut, "--host-key") || !strings.Contains(errOut, "--yes") {
		t.Errorf("host add unknown --no-input: exit %d\n%s", code, errOut)
	}
	if client.HasPin("fresh") {
		t.Error("failed host add left pin file behind")
	}

	// host add with malformed --host-key fails with exit code 2.
	if got, code := lineErr(t, "host", "add", "fresh", "rekeybox", "--ssh-config", sshCfg, "--host-key", "not-a-key"); code != 2 || !strings.Contains(got, "SHA256") {
		t.Errorf("host add bad --host-key: exit %d\n%s", code, got)
	}
	_ = brand.Name
}
