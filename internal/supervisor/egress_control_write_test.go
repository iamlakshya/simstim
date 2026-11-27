package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestQueuedEgressCancellationCannotPoisonNextRPC(t *testing.T) {
	f := eventController(t)
	turn := f.c.controlWriteTurn()
	turn <- struct{}{}
	var once sync.Once
	release := func() { once.Do(func() { <-turn }) }
	t.Cleanup(release)
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		close(started)
		result <- f.c.request(ctx, "cancelled", nil, nil)
	}()
	waitEventSignal(t, started, "queued request")
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("queued request did not cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued cancellation waited for another frame's owner")
	}
	select {
	case <-f.local.Done():
		t.Fatal("queued cancellation closed another frame's link")
	default:
	}
	release()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result <- f.c.request(ctx, "following", nil, nil)
	}()
	req := f.next(t)
	if req.Op != "following" {
		t.Fatal("cancelled queued request emitted a frame")
	}
	f.reply(t, req)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal("cancelled owner poisoned the following RPC deadline")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("following RPC did not complete")
	}
}

func TestOversizedEgressFrameRefusesBeforeWriteOwnership(t *testing.T) {
	f := eventController(t)
	turn := f.c.controlWriteTurn()
	turn <- struct{}{}
	defer func() { <-turn }()
	args := json.RawMessage("\"" + strings.Repeat("x", proto.MaxFrame) + "\"")
	err := f.c.writeControlRequest(context.Background(), proto.Request{ID: 1, Op: "probe", Args: args})
	if !errors.Is(err, proto.ErrFrameTooBig) {
		t.Fatal("oversized frame entered the owned write turn")
	}
	select {
	case <-f.local.Done():
		t.Fatal("pre-write admission refusal closed a healthy link")
	default:
	}
}

type partialEgressFrameWriter struct{ target io.Writer }

func (w partialEgressFrameWriter) Write(p []byte) (int, error) {
	n, err := w.target.Write(p[:3])
	if err != nil {
		return n, err
	}
	return n, errors.New("private-write-marker")
}

func TestPartialEgressFrameClosesOnlyCapturedOwner(t *testing.T) {
	f := eventController(t)
	a, b := net.Pipe()
	replacement := link.New(a, link.Supervisor, egress.LinkOptions())
	peer := link.New(b, link.Guest, egress.LinkOptions())
	t.Cleanup(func() { _ = replacement.Close(); _ = peer.Close() })
	f.s.mu.Lock()
	f.s.eg = &egressProc{sess: replacement}
	f.s.m.Armed = true
	f.s.mu.Unlock()
	f.c.w = proto.NewWriter(partialEgressFrameWriter{target: f.c.st})
	err := f.c.writeControlRequest(context.Background(), proto.Request{ID: 1, Op: "partial"})
	if err == nil || strings.Contains(err.Error(), "private-write-marker") {
		t.Fatal("partial control frame succeeded or echoed private writer details")
	}
	waitEventSignal(t, f.c.events.controlClosed, "old owner closure")
	select {
	case <-replacement.Done():
		t.Fatal("old writer failure closed the replacement owner")
	default:
	}
	f.s.mu.Lock()
	armed := f.s.m.Armed
	f.s.mu.Unlock()
	if !armed {
		t.Fatal("old control failure disarmed the replacement")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	local, err := replacement.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal("replacement transport did not survive the old partial frame")
	}
	remote, err := peer.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = local.Close()
	_ = remote.Close()
}

func TestOwnedEgressReaderUsesCanonicalFrameLimit(t *testing.T) {
	f := eventController(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	var decoded json.RawMessage
	go func() { result <- f.c.request(ctx, "public-state", nil, &decoded) }()
	req := f.next(t)
	public := json.RawMessage("\"" + strings.Repeat("x", maxEvent+1024) + "\"")
	if err := f.writer.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: public}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil || string(decoded) != string(public) {
			t.Fatal("owned public control response inherited the guest event cap")
		}
	case <-ctx.Done():
		t.Fatal("owned canonical-sized response was not delivered")
	}
}

func TestEgressFrameCancellationJoinsAnAlreadyRunningCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, joined := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	join := egressFrameCancellation(ctx, func() { close(entered); <-release })
	t.Cleanup(func() { unblock(); join() })
	cancel()
	waitEventSignal(t, entered, "owned deadline callback")
	started := make(chan struct{})
	go func() {
		close(started)
		join()
		close(joined)
	}()
	waitEventSignal(t, started, "callback join")
	select {
	case <-joined:
		t.Fatal("deadline ownership could pass while its callback still ran")
	case <-time.After(50 * time.Millisecond):
		// The callback is physically held at release, not merely assumed
		// to be running from a scheduler or sleep observation.
	}
	unblock()
	waitEventSignal(t, joined, "deadline callback completion before next frame")
}
