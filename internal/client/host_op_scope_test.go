package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestHostPublicationUnpinAndUnlockDoNotCancelResponseContext(t *testing.T) {
	h, owner, _, unlock := hostObservationFixture(t)
	ctx, unpin, err := holdHostPublicationContext(owner, h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer unpin()
	var request proto.Request
	c := &Conn{Host: h, pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
	c.w = proto.NewWriter(grantWriteFunc(func(p []byte) (int, error) {
		frame, err := proto.Read(bufio.NewReader(bytes.NewReader(p)))
		if err != nil {
			return 0, err
		}
		if err := json.Unmarshal(frame.Payload, &request); err != nil {
			return 0, err
		}
		return len(p), nil
	}))
	released := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- c.callWithSent(ctx, "response-context-fixture", nil, nil, func() {
			unpin()
			unlock()
			close(released)
		})
	}()
	hostStateLifetimeAwait(t, released)
	if err := ctx.Err(); err != nil {
		t.Fatalf("first-publication release cancelled the still-pending RPC: %v", err)
	}
	_, nextUnlock, err := LockHost(t.Context(), h.Name, "after publication")
	if err != nil {
		t.Fatal(err)
	}
	nextUnlock()
	c.mu.Lock()
	pending := c.pending[request.ID]
	c.mu.Unlock()
	if pending == nil {
		t.Fatal("the request stopped before its actual response")
	}
	pending <- proto.Response{ID: request.ID, OK: true}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestHostOwnedMutationRefusesSameOwnerPublicationAndBulkCannotBypass(t *testing.T) {
	h, owner, _, _ := hostObservationFixture(t)
	_, unpin, err := holdHostPublicationContext(owner, h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer unpin()
	// Same goroutine and no derived publication token: metadata must fail
	// busy rather than waiting for its own deferred unpin.
	mutationCtx, cancel := context.WithTimeout(owner, time.Second)
	defer cancel()
	var user *UserError
	if err := UpdateHosts(mutationCtx, HostChange{Kind: "owned metadata mutation", Was: &h, Now: &h}); !errors.As(err, &user) || user.Code != "host_busy" {
		t.Fatalf("metadata attempted a shared-to-exclusive upgrade: %v", err)
	}
	changedHost := cloneObservationHost(h)
	changedHost.SSH = "replacement.example"
	if err := SaveHosts(owner, []Host{changedHost}); !errors.As(err, &user) || user.Code != "host_busy" {
		t.Fatalf("bulk treated the caller's held context as a probe bypass: %v", err)
	}
	unpin()
	if err := UpdateHosts(owner, HostChange{Kind: "owned metadata mutation", Was: &h, Now: &h}); err != nil {
		t.Fatal("actual mutation failed after the ordinary participant left:", err)
	}
}
