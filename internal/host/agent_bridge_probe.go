package host

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/iamlakshya/simstim/internal/agentbridge"
)

// probeNativeBinding triggers the owned SSH client's private collector.
// It writes no bytes and accepts only EOF. No native path is published.
func (b *hostAgentBridge) probeNativeBinding() error {
	b.mu.Lock()
	if b.closed || b.probeStarted {
		b.mu.Unlock()
		return nativeBindingProbeError()
	}
	b.probeStarted = true
	b.mu.Unlock()
	path, err := nativeBindingSocket()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(agentbridge.HandshakeTimeout)
	if !b.bindDeadline.IsZero() && b.bindDeadline.Before(deadline) {
		deadline = b.bindDeadline
	}
	ctx, cancel := context.WithDeadline(b.ctx, deadline)
	defer cancel()
	if err := nativeProbeContextError(b.ctx, ctx); err != nil {
		return err
	}
	var linkDone <-chan struct{}
	if b.link != nil {
		linkDone = b.link.Done()
		select {
		case <-linkDone:
			return context.Canceled
		default:
		}
	}
	dial := b.probeDial
	if dial == nil {
		dial = dialNativeBinding
	}
	probe, err := dial(ctx, path)
	if err != nil {
		if b.ctx.Err() != nil {
			return b.ctx.Err()
		}
		return nativeBindingProbeError()
	}
	defer probe.Close()
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return context.Canceled
	}
	b.probe = probe
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.probe == probe {
			b.probe = nil
		}
		b.mu.Unlock()
	}()
	return waitNativeProbeEOF(b.ctx, ctx, linkDone, probe)
}

func dialNativeBinding(ctx context.Context, path string) (net.Conn, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, nativeBindingProbeError()
	}
	return connection, nil
}

func nativeBindingSocket() (string, error) {
	path := os.Getenv("SSH_AUTH_SOCK")
	if !filepath.IsAbs(path) {
		return "", nativeBindingProbeError()
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || !privateAgentSocket(info) {
		return "", nativeBindingProbeError()
	}
	return path, nil
}

func waitNativeProbeEOF(parent, ctx context.Context, linkDone <-chan struct{}, probe net.Conn) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nativeBindingProbeError()
	}
	if err := probe.SetReadDeadline(deadline); err != nil {
		return nativeBindingProbeError()
	}
	finished, watcherDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			_ = probe.Close()
		case <-linkDone:
			_ = probe.Close()
		case <-finished:
		}
	}()
	defer func() { close(finished); <-watcherDone }()
	var data [1]byte
	n, err := probe.Read(data[:])
	if contextErr := nativeProbeContextError(parent, ctx); contextErr != nil {
		return contextErr
	}
	if n != 0 || err != io.EOF {
		return nativeBindingProbeError()
	}
	return nil
}

func nativeProbeContextError(parent, ctx context.Context) error {
	if parent.Err() != nil {
		return parent.Err()
	}
	if ctx.Err() != nil {
		return nativeBindingProbeError()
	}
	return nil
}

func nativeBindingProbeError() error {
	return UserError("ssh_agent_refused", "the host could not finish the native binding probe",
		"Update the client and OpenSSH, then attach again.")
}
