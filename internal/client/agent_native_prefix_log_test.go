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

func TestValidNativePrefixCannotAcknowledgeAnOverflowedCapture(t *testing.T) {
	agentClientEnv(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	lease, err := agentbridge.NewLease()
	if err != nil {
		t.Fatal(err)
	}
	c, err := newAgentBindingCollector(ctx, agentbridge.Scope{Session: "abc234de", Lease: lease})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { c.Close(); _ = c.log.Close() }()
	// Negative owner fixture only. Actual native provenance is independent.
	c.mu.Lock()
	c.ownerCtx, c.ownerPID = ctx, os.Getpid()
	c.owner = &sshProc{rawDone: make(chan struct{})}
	c.mu.Unlock()
	if err := c.log.AttachOwner(func(reason error) { c.fail(reason) }); err != nil {
		t.Fatal(err)
	}
	close(c.ownerReady)
	connection, err := net.DialTimeout("unix", c.socket.path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	wire, peer := nativeBindingFixture(t, true)
	if err := verifyNativeAgentRecord(wire, peer); err != nil {
		t.Fatal(err)
	}
	prefix := "debug1: OpenSSH_10.6p2, generated capture fixture\r\n" +
		"debug1: Server host certificate: " + peer.keyType + " " + peer.fingerprint + `, serial 0 ID "display tail` + "\r\n"
	data := prefix + strings.Repeat("bounded native transcript row\n", agentNativeLogTotal/len("bounded native transcript row\n")+2)
	writer := nativeTestWriter(t, c.log)
	written := make(chan struct{})
	go func() { _, _ = io.WriteString(writer, data); close(written) }()
	waitNativeLog(t, func() bool { return c.log.Failure() != nil })
	select {
	case <-written:
	case <-ctx.Done():
		t.Fatal("capture overflow did not release its owned writer")
	}
	_, _ = connection.Write(wire)
	var reply [5]byte
	n, err := io.ReadFull(connection, reply[:])
	var timeout net.Error
	if err == nil || n != 0 || errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("overflowed capture acknowledged a valid signed record: bytes=%x error=%v", reply[:n], err)
	}
	if err := c.wait(ctx); err == nil {
		t.Fatal("a failed complete-prefix capture became ready")
	}
}
