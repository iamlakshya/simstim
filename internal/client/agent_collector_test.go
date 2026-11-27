//go:build darwin || linux

package client

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

// These negative fixtures assign an internal owner directly. They prove
// rejection ordering, never native provenance. The positive native hook
// witness is TestNativeCollectorRealOpenSSH.
func TestCollectorRejectsBeforeACKAndNeverAcceptsLaterHostData(t *testing.T) {
	agentClientEnv(t)
	for _, failure := range []string{"missing process", "exited process", "wrong process", "8.0", "8.8", "binding error", "wrong signature", "wrong key class"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			lease, err := agentbridge.NewLease()
			if err != nil {
				t.Fatal(err)
			}
			collector, err := newAgentBindingCollector(ctx, agentbridge.Scope{Session: "abc234de", Lease: lease})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { collector.Close(); _ = collector.log.Close() }()
			collector.mu.Lock()
			collector.ownerCtx, collector.ownerPID = ctx, os.Getpid()
			if failure != "missing process" {
				collector.owner = &sshProc{rawDone: make(chan struct{})}
				if failure == "exited process" {
					close(collector.owner.rawDone)
				}
			}
			if failure == "wrong process" {
				collector.ownerPID++
			}
			collector.mu.Unlock()
			if err := collector.log.AttachOwner(func(error) {}); err != nil {
				t.Fatal(err)
			}
			close(collector.ownerReady)
			wire, peer := nativeBindingFixture(t, false)
			version := "10.2"
			if failure == "8.0" || failure == "8.8" {
				version = failure
			}
			prefix := "debug1: OpenSSH_" + version + "p1, rejection fixture\r\n" +
				"debug1: Server host key: " + peer.keyType + " " + peer.fingerprint + "\r\n"
			if failure == "binding error" {
				prefix += "debug2: get_agent_identities: ssh_agent_bind_hostkey: invalid format\r\n"
			}
			if failure == "wrong signature" {
				wire[len(wire)-2] ^= 1
			}
			if failure == "wrong key class" {
				prefix = strings.Replace(prefix, peer.keyType, "ssh-ed25519-cert-v01@openssh.com", 1)
			}
			writer, err := os.OpenFile(collector.log.Path(), os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if _, err := io.WriteString(writer, prefix); err != nil {
				t.Fatal(err)
			}
			connection, err := net.DialTimeout("unix", collector.socket.path, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			// The write may lose the race to the intentional peer refusal.
			_, _ = connection.Write(wire)
			var reply [5]byte
			if n, err := io.ReadFull(connection, reply[:]); err == nil || n != 0 {
				t.Fatalf("invalid native proof was acknowledged: %x, %v", reply[:n], err)
			} else {
				var timeout net.Error
				if errors.As(err, &timeout) && timeout.Timeout() {
					t.Fatal("invalid proof did not close its owned connection")
				}
			}
			if err := collector.wait(ctx); err == nil {
				t.Fatal("invalid proof became ready")
			}
			second, err := net.DialTimeout("unix", collector.socket.path, 100*time.Millisecond)
			if err == nil {
				_ = second.Close()
				t.Fatal("a failed first attempt left a listener for later host data")
			}
		})
	}
}

func TestCollectorCleanupPreservesReplacedSocketAndDirectory(t *testing.T) {
	agentClientEnv(t)
	socket, err := newAgentCollectorSocket()
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	if err := os.Remove(socket.path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socket.path, []byte("foreign replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	socket.Close()
	data, err := os.ReadFile(socket.path)
	if err != nil || string(data) != "foreign replacement" {
		t.Fatalf("collector removed its socket replacement: %q, %v", data, err)
	}
	// The replacement prevents directory removal. The fixture owns it.
	if err := os.Remove(socket.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(strings.TrimSuffix(socket.path, "/b.sock")); err != nil {
		t.Fatal(err)
	}
}
