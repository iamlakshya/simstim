package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestRelaySpaceWaitSharesTheFrameDeadline(t *testing.T) {
	s := &Supervisor{relayUsed: maxRelayBytes}
	c := &client{done: make(chan struct{})}
	finished := make(chan error, 1)
	deadline := time.Now().Add(100 * time.Millisecond)
	go func() { finished <- s.takeRelay(c, keepBuf+1, deadline) }()
	select {
	case err := <-finished:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("a full relay pool returned %v, want the frame deadline", err)
		}
	case <-time.After(time.Second):
		close(c.done)
		<-finished
		t.Fatal("a frame waited past its deadline for relay space")
	}
	s.relayMu.Lock()
	used := s.relayUsed
	s.relayMu.Unlock()
	if used != maxRelayBytes {
		t.Fatalf("an expired frame reserved relay space: %d", used)
	}
	// A wake at the deadline must not reserve space after the timer expired.
	s.giveRelay(maxRelayBytes)
	if err := s.takeRelay(c, keepBuf+1, deadline); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("an already expired frame took free relay space: %v", err)
	}
}

// An available pool does not give the payload a new frame timeout. The
// absolute deadline below is the one already set before reading the header.
func TestRelaySpaceWaitDoesNotRenewThePayloadDeadline(t *testing.T) {
	left, right := net.Pipe()
	local := link.New(left, link.Supervisor, link.Options{})
	remote := link.New(right, link.Guest, link.Options{})
	t.Cleanup(func() { _ = local.Close(); _ = remote.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	accepted := make(chan *link.Stream, 1)
	go func() { st, _ := remote.Accept(ctx); accepted <- st }()
	st, err := local.Open(ctx, link.KindHolder, nil)
	if err != nil {
		t.Fatal(err)
	}
	if <-accepted == nil {
		t.Fatal("the holder stream was not accepted")
	}
	server, peer := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = peer.Close() })
	c := &client{conn: server, w: proto.NewWriter(server), done: make(chan struct{})}
	s := &Supervisor{link: local, log: slog.New(slog.DiscardHandler), relayUsed: maxRelayBytes}
	deadline := time.Now().Add(150 * time.Millisecond)
	_ = st.SetReadDeadline(deadline)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		s.relayBody(c, st, bufio.NewReader(st), proto.TSnapshot, keepBuf+1, nil, deadline, 10*time.Second)
	}()
	defer func() { _ = local.Close(); _ = peer.Close(); <-finished }()
	// Wait for the real relay path to enter its shared-budget wait, then
	// release that budget while the same payload deadline remains in force.
	waitFor(t, "the relay space wait", func() bool {
		s.relayMu.Lock()
		defer s.relayMu.Unlock()
		return s.relayWake != nil
	})
	s.giveRelay(maxRelayBytes)
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	f, err := proto.Read(bufio.NewReader(peer))
	var pe proto.Error
	if err != nil || f.Type != proto.TError || json.Unmarshal(f.Payload, &pe) != nil || pe.Code != "guest_frame" {
		t.Fatalf("an incomplete payload received another timeout after relay space: type=%v error=%v payload=%s", f.Type, err, f.Payload)
	}
	<-finished
	s.relayMu.Lock()
	used := s.relayUsed
	s.relayMu.Unlock()
	if used != 0 {
		t.Fatalf("the timed-out payload retained %d relay bytes", used)
	}
}
