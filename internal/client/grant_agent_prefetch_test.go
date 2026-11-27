//go:build darwin || linux

package client

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

func TestFinalNewAgentGrantUsesMetadataWithoutAnotherHealthDial(t *testing.T) {
	h := h5GrantState(t)
	dir, err := os.MkdirTemp("/tmp", "gh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "health.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	t.Setenv("SSH_AUTH_SOCK", path)
	observed := make(chan error, 1)
	finished := make(chan struct{})
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("the owned health observer did not finish")
		}
	})
	go func() {
		defer close(finished)
		conn, err := listener.AcceptUnix()
		if err == nil {
			_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			var protocol [1]byte
			if n, readErr := conn.Read(protocol[:]); n != 0 || readErr != io.EOF {
				err = missingLocalAgent()
			}
			_ = conn.Close()
		}
		_ = listener.Close()
		observed <- err
	}()
	c := &Conn{Host: h, hostID: strings.Repeat("a", 32), Features: []string{"session.id", agentbridge.Feature}}
	c.route.t = &SSH{Host: h}
	ctx := WithGrantRequest(t.Context(), NewGrantRequest(false, true))
	if err := c.CheckNewGrants(ctx, nil, true); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal("preflight wrote protocol bytes or did not close its generated socket", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the single preflight health connection did not finish")
	}
	// The owned socket inode remains, but the listener is now closed.
	// A second health dial in final publication would deterministically fail.
	locked, release, err := c.beginGrant(ctx, "new", true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	args, _, err := c.prepareNewGrants(locked, NewArgs{SSHAgent: true})
	if err != nil || !args.SSHAgent || args.ID == "" {
		t.Fatalf("final metadata recheck opened another health connection: args=%+v error=%v", args, err)
	}
}
