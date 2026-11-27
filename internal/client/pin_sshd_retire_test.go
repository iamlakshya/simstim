//go:build !windows

package client

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRealSSHDForgetRetiresWithoutPinOrConfig(t *testing.T) {
	if os.Getenv("PIN_SSHD") != "1" || os.Getuid() != 0 {
		t.Skip("needs the root localhost sshd test container")
	}
	for _, damage := range []string{"empty-pin", "missing-config", "unlisted", "post-pin-cancel", "post-rekey-cancel"} {
		t.Run(damage, func(t *testing.T) {
			dir := sshdHome(t)
			keygen(t, dir, "-q", "-t", "ed25519", "-N", "", "-C", "", "-f", filepath.Join(dir, "id"))
			auth := putFile(t, dir, "authorized_keys", pubKey(t, filepath.Join(dir, "id.pub"))+"\n")
			server := realSSHD(t, dir, "host-key", "AuthorizedKeysFile "+auth, "PermitRootLogin prohibit-password")
			cfg := putFile(t, dir, "client.conf", fmt.Sprintf("Host testbox\n HostName 127.0.0.1\n Port %d\n User root\n IdentityFile %s\n IdentitiesOnly yes\n BatchMode yes\n", server.port, filepath.Join(dir, "id")))
			h := Host{Name: "box", SSH: "testbox", SSHConfig: cfg}
			if err := SaveHosts(context.Background(), []Host{h}); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if _, err := PinHostKey(ctx, h, "", FirstUseAccept); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = retireControls(ctx, h.Name) })
			if out, err := (&SSH{Host: h}).Run(ctx, "printf READY", nil); err != nil || string(out) != "READY" {
				t.Fatalf("start master: %q, %v", out, err)
			}
			running, pid := (&SSH{Host: h}).CheckMaster(ctx)
			if !running || pid <= 0 {
				t.Fatal("master did not start")
			}
			var retirementErr error
			switch damage {
			case "empty-pin":
				if err := os.WriteFile(pinPath(h.Name), []byte(pinHeader(h.Name)+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-config":
				if err := os.Remove(cfg); err != nil {
					t.Fatal(err)
				}
			case "post-pin-cancel", "post-rekey-cancel":
				retirementErr = func() error {
					if damage == "post-rekey-cancel" {
						if err := writeTestPin(h.Name, []hostKey{newKey(t, "ssh-ed25519")}); err != nil {
							return err
						}
					}
					operation, cancel := context.WithCancel(ctx)
					defer cancel()
					previous := afterPinCommit
					afterPinCommit = cancel
					defer func() { afterPinCommit = previous }()
					var err error
					if damage == "post-pin-cancel" {
						_, err = PinHostKey(operation, h, "", FirstUseAccept)
					} else {
						_, _, err = Rekey(operation, h, "", func(string, string) error { return nil })
					}
					if operation.Err() == nil {
						t.Error("post-commit cancellation hook did not run")
					}
					return err
				}()
			case "unlisted":
				if err := SaveHosts(context.Background(), nil); err != nil {
					t.Fatal(err)
				}
			}
			if !strings.HasPrefix(damage, "post-") {
				retirementErr = ForgetHostContext(ctx, h.Name)
			}
			err := retirementErr
			if damage == "unlisted" {
				if err == nil {
					t.Fatal("missing host did not report not found")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			end := time.Now().Add(2 * time.Second)
			for {
				state, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
				if os.IsNotExist(readErr) {
					break
				}
				if readErr != nil {
					t.Fatal(readErr)
				}
				_, tail, _ := strings.Cut(string(state), ") ")
				if strings.HasPrefix(tail, "Z ") {
					break
				}
				if time.Now().After(end) {
					t.Fatalf("master still alive after %s forget: %s", damage, state)
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}
