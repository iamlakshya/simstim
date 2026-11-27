package client

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestGitSSHConfigCannotLoseItsPinOrHardening(t *testing.T) {
	sshDirs(t)
	pinFor(t, "box")
	h := Host{Name: "box", SSH: "box.example"}
	args, err := (&SSH{Host: h}).connArgs(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	args = append(args, NoPortForwards()...)
	args = append(args, "-E", filepath.Join(dir, "ssh.log"))
	base := gitSSHConfig{Version: 1, Name: h.Name, Args: args, Target: h.SSH, Root: "/repo"}
	if err := validateGitSSHConfig(base, dir); err != nil {
		t.Fatalf("valid snapshot: %v", err)
	}
	for _, kind := range []string{"missing-pin", "first-value", "joined-option", "missing-args", "wrong-alias", "proxy-command", "known-hosts-command", "pin-none", "foreign-config"} {
		t.Run(kind, func(t *testing.T) {
			cfg := base
			cfg.Args = slices.Clone(args)
			switch kind {
			case "missing-pin":
				for i, a := range cfg.Args {
					if a == "StrictHostKeyChecking=yes" {
						cfg.Args[i] = "StrictHostKeyChecking=no"
					}
				}
			case "first-value":
				cfg.Args = append([]string{"-o", "ForwardAgent=yes"}, cfg.Args...)
			case "joined-option":
				cfg.Args = append([]string{"-oForwardAgent=yes", "ignored"}, cfg.Args...)
			case "missing-args":
				cfg.Args = nil
			case "wrong-alias":
				cfg.Name = "another"
			case "pin-none":
				for i, a := range cfg.Args {
					if strings.HasPrefix(a, "UserKnownHostsFile=") {
						cfg.Args[i] = "UserKnownHostsFile=none"
					}
				}
			case "foreign-config":
				cfg.Args[1] = "/unexpected/config"
			case "proxy-command":
				cfg.Args = append(cfg.Args, "-o", "ProxyCommand=unexpected-command")
			case "known-hosts-command":
				cfg.Args = append(cfg.Args, "-o", "KnownHostsCommand=unexpected-command")
			}
			if err := validateGitSSHConfig(cfg, dir); err == nil {
				t.Fatal("accepted damaged SSH snapshot")
			}
		})
	}
}

func TestGitSSHStatusRequiresOneCompleteRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	for _, body := range []string{`{"exited":true,"code":255,"stopped":false}`, `{"exited":true,"code":255} trailing`, `{"exited":true,"code":255,"extra":1}`, `{"exited":true,"code":999}`, `{"exited":true,"code":255}`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, ok := readGitSSHStatus(path)
		if ok != (body == `{"exited":true,"code":255,"stopped":false}`) {
			t.Fatalf("status %q accepted=%v", body, ok)
		}
	}
}

func TestGitSSHLeaseLossCancelsItsOwnedOperation(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "stale"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lease")
			if kind != "missing" {
				var b [8]byte
				binary.LittleEndian.PutUint64(b[:], 1)
				data := b[:]
				if kind == "corrupt" {
					data = []byte("bad")
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			defer close(done)
			go watchGitLease(cancel, path, done, 20*time.Millisecond)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("lost lease did not stop operation")
			}
		})
	}
	if gitSSHLeaseTimeout != 30*time.Second {
		t.Fatal("production lease bound changed")
	}
}

func TestGitSSHConfigRejectsMalformedRecords(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	for _, body := range []string{`{"version":1}`, `{"version":2,"name":"box","args":[]}`, `{"version":1} trailing`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readGitSSHConfig(path, gitSnapshotDigest([]byte(body))); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func gitSnapshotDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func TestGitSSHConfigRequiresTheProducerDigest(t *testing.T) {
	sshDirs(t)
	pinFor(t, "box")
	h := Host{Name: "box", SSH: "box.example"}
	args, err := (&SSH{Host: h}).connArgs(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	args = append(args, NoPortForwards()...)
	args = append(args, "-E", filepath.Join(dir, "ssh.log"))
	body, err := json.Marshal(gitSSHConfig{Version: 1, Name: h.Name, Args: args, Target: h.SSH, Root: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := gitSnapshotDigest(body)
	if _, err := readGitSSHConfig(path, digest); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", strings.Repeat("0", 64), digest[:63]} {
		if _, err := readGitSSHConfig(path, bad); err == nil {
			t.Fatal("accepted missing or changed producer digest")
		}
	}
	if err := os.WriteFile(path, append(body, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readGitSSHConfig(path, digest); err == nil {
		t.Fatal("accepted changed snapshot bytes")
	}
}

func TestGitSSHInvocationRequiresOneUploadPackCommand(t *testing.T) {
	for _, command := range []string{remoteUploadPack + " '/repo'", remoteUploadPack + ` '/path with '\''quote'`} {
		if err := validateGitSSHInvocation([]string{"box.example", command}); err != nil {
			t.Fatalf("valid command %q: %v", command, err)
		}
	}
	for _, command := range []string{remoteUploadPack + " '/repo'; printf unexpected", remoteUploadPack + ` '/repo'; printf 'unexpected'`, remoteUploadPack + " /repo", "git-upload-pack '/repo'", "other-upload-pack '/repo'"} {
		if err := validateGitSSHInvocation([]string{"box.example", command}); err == nil {
			t.Fatalf("accepted extra command %q", command)
		}
	}
}
