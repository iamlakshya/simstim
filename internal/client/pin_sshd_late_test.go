//go:build !windows

package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealSSHDLateMasterDoesNotSurviveForget(t *testing.T) {
	if os.Getenv("PIN_SSHD") != "1" || os.Getuid() != 0 {
		t.Skip("needs the root localhost sshd test container")
	}
	dir := sshdHome(t)
	keygen(t, dir, "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", filepath.Join(dir, "id"))
	auth := putFile(t, dir, "authorized_keys", pubKey(t, filepath.Join(dir, "id.pub"))+"\n")
	ready, release := filepath.Join(dir, "auth-ready"), filepath.Join(dir, "auth-release")
	// sshd requires the command's ancestors to be root-owned and not
	// writable by others, so its executable cannot live below /tmp.
	safeDir, err := os.MkdirTemp("/root", "pin-authorize-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(safeDir) })
	script := filepath.Join(safeDir, "authorize")
	body := "#!/bin/sh\ntouch " + shellWord(ready) + "\nwhile [ ! -f " + shellWord(release) + " ]; do sleep 0.01; done\ncat " + shellWord(auth) + "\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	server := realSSHD(t, dir, "host-key", "AuthorizedKeysFile none", "AuthorizedKeysCommand "+script, "AuthorizedKeysCommandUser root", "PermitRootLogin prohibit-password")
	cfg := putFile(t, dir, "client.conf", fmt.Sprintf("Host testbox\n HostName 127.0.0.1\n Port %d\n User root\n IdentityFile %s\n IdentitiesOnly yes\n BatchMode yes\n", server.port, filepath.Join(dir, "id")))
	h := Host{Name: "box", SSH: "testbox", SSHConfig: cfg}
	ctx := context.Background()
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if _, err := PinHostKey(ctx, h, "", FirstUseAccept); err != nil {
		t.Fatal(err)
	}
	ssh := &SSH{Host: h}
	prepared, err := ssh.Command(ctx, "printf WITNESS", "-T")
	if err != nil {
		t.Fatal(err)
	}
	path := sharedPath(prepared.Args)
	prefix := strings.TrimSuffix(filepath.Base(path), "%C")
	controlDir := filepath.Dir(path)
	first := make(chan error, 1)
	go func() { _, err := ssh.Run(ctx, "sleep 3", nil); first <- err }()
	for until := time.Now().Add(5 * time.Second); ; {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(until) {
			debugCtx, cancel := context.WithTimeout(ctx, time.Second)
			log, debugErr := ssh.VerboseLog(debugCtx)
			cancel()
			t.Fatalf("authentication gate was not reached: %v\n%s", debugErr, log)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := ForgetHost(h.Name); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeWrapper(h); err != nil {
		t.Fatal(err)
	}
	late := false
	for until := time.Now().Add(time.Second); time.Now().Before(until); {
		entries, _ := os.ReadDir(controlDir)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), prefix) && controlSocketRE.MatchString(entry.Name()) {
				late = true
			}
		}
		if late {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if late {
		out, err := prepared.CombinedOutput()
		if err == nil || strings.Contains(string(out), "WITNESS") {
			t.Errorf("a master created after forget authenticated a prepared command: %q, %v", out, err)
		}
	}
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("first SSH did not finish")
	}
}
