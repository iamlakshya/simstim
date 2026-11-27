package link

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

func TestPeerGoneWaitPreservesHalfCloseThenSeesFullClose(t *testing.T) {
	ctx := testCtx(t)
	supervisor, guest := pair(t, Options{})
	local, err := supervisor.Open(ctx, KindGet, nil)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := guest.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- remote.WaitPeerGone(ctx) }()
	if err := local.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Read(make([]byte, 1)); !errors.Is(err, io.EOF) || !remote.PeerWriteClosed() {
		t.Fatalf("valid empty half-close = %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("half-close became peer disappearance: %v", err)
	default:
	}
	if err := local.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("actual full close did not wake the helper owner")
	}
}

func TestPeerGoneWaitEndsOnAbortAndCancellation(t *testing.T) {
	for _, abort := range []bool{false, true} {
		supervisor, guest := pair(t, Options{})
		local, err := supervisor.Open(testCtx(t), KindGet, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer local.Close()
		remote, err := guest.Accept(testCtx(t))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- remote.WaitPeerGone(ctx) }()
		if abort {
			guest.AbortStreams()
		} else {
			cancel()
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("revocation was reported as a clean peer close")
			}
		case <-time.After(time.Second):
			t.Fatal("revocation left peer wait blocked")
		}
		cancel()
	}
}
