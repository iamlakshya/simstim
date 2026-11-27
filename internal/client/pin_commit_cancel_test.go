package client

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestCommittedPinRetiresMastersAfterCancellation(t *testing.T) {
	if !controlMaster {
		t.Skip("OpenSSH connection sharing is unavailable")
	}
	for _, rekey := range []bool{false, true} {
		name := "pin"
		if rekey {
			name = "rekey"
		}
		t.Run(name, func(t *testing.T) {
			dir := fakeSSH(t)
			h := Host{Name: "box", SSH: "box.example"}
			if err := SaveHosts(context.Background(), []Host{h}); err != nil {
				t.Fatal(err)
			}
			old, next := newKey(t, "ssh-ed25519"), newKey(t, "ssh-ed25519")
			if rekey {
				if err := writeTestPin(h.Name, []hostKey{old}); err != nil {
					t.Fatal(err)
				}
			}
			args, err := (&SSH{Host: h}).baseArgs()
			if err != nil {
				t.Fatal(err)
			}
			socket := expandedTestSocket(t, args)
			if err := os.WriteFile(socket, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			shows(t, dir, next)
			put(t, dir, "trust-log", trustLog(next, "unknown"))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			previous := afterPinCommit
			t.Cleanup(func() { afterPinCommit = previous })
			afterPinCommit = cancel
			if rekey {
				_, _, err = Rekey(ctx, h, next.Fingerprint(), func(string, string) error { return nil })
			} else {
				_, err = PinHostKey(ctx, h, next.Fingerprint(), FirstUseAccept)
			}
			if err != nil {
				t.Fatalf("committed operation: %v", err)
			}
			if ctx.Err() == nil {
				t.Fatal("cancellation hook did not run")
			}
			if _, err := os.Lstat(socket); !os.IsNotExist(err) {
				t.Fatalf("old socket remains: %v", err)
			}
			stopped := false
			for _, call := range sshCalls(t, dir) {
				if strings.Contains(call, " -O exit -- placeholder") {
					stopped = true
				}
			}
			if !stopped {
				t.Fatal("committed pin skipped concrete master retirement after cancellation")
			}
			keys, err := readPin(h.Name)
			if err != nil || len(keys) != 1 || keys[0].Fingerprint() != next.Fingerprint() {
				t.Fatalf("committed pin lost: %v, %v", keys, err)
			}
		})
	}
}
