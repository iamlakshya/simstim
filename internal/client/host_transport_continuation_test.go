package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func continuationHelloProc(t *testing.T, hostID string) (*startupProc, *io.PipeWriter) {
	t.Helper()
	p := newStartupProc(t, nil, nil)
	var hello bytes.Buffer
	if err := proto.NewWriter(&hello).WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol,
		Version: version.Version, Role: "bridge", HostID: hostID, Features: []string{"session.id"}}); err != nil {
		t.Fatal(err)
	}
	reader, output := io.Pipe()
	t.Cleanup(func() { _ = reader.Close(); _ = output.Close() })
	p.out = io.MultiReader(&hello, reader)
	return p, output
}

func TestActualContinuationDialReleasesHostLockAcrossChildStartup(t *testing.T) {
	h := h5GrantState(t)
	id := strings.Repeat("a", 32)
	parentProc, parentOutput := continuationHelloProc(t, id)
	childProc, childOutput := continuationHelloProc(t, id)
	var starts atomic.Int32
	transport := &lifecycleTransport{start: func(ctx context.Context) (Proc, error) {
		if starts.Add(1) == 1 {
			return parentProc, nil
		}
		if ctx.Value(heldKey{h.Name}) != nil || ctx.Value(hostStateObservationKey{}) != nil {
			return nil, errors.New("child startup retained a released or held publication context")
		}
		_, release, err := LockHost(t.Context(), h.Name, "mutation during child startup")
		if err != nil {
			return nil, err
		}
		release()
		return childProc, nil
	}}
	parent, err := dial(t.Context(), h, route{t: transport})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parentOutput.Close(); parent.Close() }()
	child, err := dialHostContinuation(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = childOutput.Close(); child.Close() }()
	if starts.Load() != 2 || !sameHostTransportSource(parent.hostTransport.source, child.hostTransport.source) ||
		parent.hostTransport.op.Identity != child.hostTransport.op.Identity || parent.hostTransport.parent.Identity != child.hostTransport.parent.Identity {
		t.Fatal("continuation refreshed original Source or actual native references")
	}
}

func TestActualContinuationCannotAdoptSourceChangedDuringChildStartup(t *testing.T) {
	h := h5GrantState(t)
	id := strings.Repeat("a", 32)
	parentProc, parentOutput := continuationHelloProc(t, id)
	childProc, childOutput := continuationHelloProc(t, id)
	var starts atomic.Int32
	entered, allowHello := make(chan struct{}), make(chan struct{})
	transport := &lifecycleTransport{start: func(ctx context.Context) (Proc, error) {
		if starts.Add(1) == 1 {
			return parentProc, nil
		}
		close(entered)
		select {
		case <-allowHello:
			return childProc, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	parent, err := dial(t.Context(), h, route{t: transport})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parentOutput.Close(); parent.Close(); _ = childOutput.Close() }()
	done := make(chan error, 1)
	go func() {
		child, err := dialHostContinuation(t.Context(), parent)
		if child != nil {
			child.Close()
		}
		done <- err
	}()
	startupSignal(t, entered, "detached child network startup")
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	close(allowHello)
	if err := startupError(t, done); err == nil {
		t.Fatal("same HID after remove/re-add refreshed the continuation Source")
	}
	if starts.Load() != 2 {
		t.Fatal("failed child startup retried under a fresh Source")
	}
	if child, err := dialHostContinuation(t.Context(), parent); err == nil || child != nil || starts.Load() != 2 {
		t.Fatal("stale parent reached another child network startup", err)
	}
}
