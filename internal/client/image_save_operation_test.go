package client

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestImageSaveOperationRetainsOriginalRequestConnectionAndArguments(t *testing.T) {
	h := h5GrantState(t)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", "image.save")
	r := NewImageSaveGrantRequest()
	ctx := WithGrantRequest(t.Context(), r)
	defer r.Close()
	op, err := newImageSaveOperation(ctx, c, "root2345", "devtools", false)
	if err != nil {
		t.Fatal(err)
	}
	same, err := newImageSaveOperation(ctx, c, "root2345", "devtools", false)
	if err != nil || same != op {
		t.Fatal("same logical save lost its original operation", err)
	}
	for _, changed := range []struct {
		id, ref string
		replace bool
	}{{"other234", "devtools", false}, {"root2345", "other-tools", false}, {"root2345", "devtools", true}} {
		if _, err := newImageSaveOperation(ctx, c, changed.id, changed.ref, changed.replace); err == nil {
			t.Fatal("changed image-save arguments refreshed the original operation")
		}
	}
	other := NewImageSaveGrantRequest()
	otherCtx := WithGrantRequest(t.Context(), other)
	defer other.Close()
	if err := op.check(otherCtx); err == nil {
		t.Fatal("another request borrowed the image-save Source")
	}
	if err := requireGrantOperation(ctx, grantOpNew); err == nil {
		t.Fatal("image-save request donated permission to a session start")
	}
	before := c.hostTransport
	c.hostTransport = nil
	if err := op.check(ctx); err == nil {
		t.Fatal("lost original accepted connection was renewed")
	}
	c.hostTransport = before
	if err := op.check(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestImageSaveCancellationAllowsOnlyOriginalCleanupBeforeRequestClose(t *testing.T) {
	h := h5GrantState(t)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), "session.id", "image.save")
	parent, cancel := context.WithCancel(t.Context())
	r := NewImageSaveGrantRequest()
	ctx := WithGrantRequest(parent, r)
	defer r.Close()
	op, err := newImageSaveOperation(ctx, c, "root2345", "devtools", false)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	cleanup := context.WithoutCancel(ctx)
	if err := op.check(cleanup); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled original lifetime was renewed for keep", err)
	}
	owned, release, err := op.beginCleanup(cleanup)
	if err != nil {
		t.Fatal("original bounded drop cleanup lost its Source", err)
	}
	if grantRequestOf(owned) != r {
		t.Fatal("cleanup manufactured another request")
	}
	release()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, release, err := op.beginCleanup(cleanup); err == nil {
		release()
		t.Fatal("closed request regained image-save cleanup permission")
	}
}
