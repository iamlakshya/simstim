package link

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func TestBridgeOpenerAllKindsAndVersionMismatch(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	// Host side accepts a peer whose helper version differs when AllowVersionMismatch is set.
	sup := New(c1, Supervisor, Options{
		Opener:               BridgeOpener,
		AllowVersionMismatch: true,
	})
	defer sup.Close()

	// Manually send a guest-role hello carrying a different helper version.
	helloPayload, err := json.Marshal(proto.Hello{
		Magic:    proto.Magic,
		Protocol: version.Protocol,
		Version:  "0.0.1-other",
		Role:     Guest.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	clientSide := New(c2, Guest, Options{
		Opener:               BridgeOpener,
		AllowVersionMismatch: true,
	})
	defer clientSide.Close()
	_ = helloPayload // clientSide sends its own hello; verify below with raw mismatch too.

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Verify Supervisor can open every BridgeOpener kind including KindRPort and KindEvents.
	for _, k := range []Kind{KindExec, KindRPort, KindSFTP, KindPort, KindEvents} {
		st, err := sup.Open(ctx, k, map[string]string{"id": string(k)})
		if err != nil {
			t.Fatalf("sup.Open(%s): %v", k, err)
		}
		got, err := clientSide.Accept(ctx)
		if err != nil {
			t.Fatalf("client.Accept(%s): %v", k, err)
		}
		if got.Kind() != k {
			t.Fatalf("kind = %q, want %q", got.Kind(), k)
		}
		_ = st.Close()
		_ = got.Close()
	}

	// Verify raw mismatched version passes when AllowVersionMismatch is true.
	p1, p2 := net.Pipe()
	defer p1.Close()
	defer p2.Close()
	s2 := New(p1, Supervisor, Options{
		Opener:               BridgeOpener,
		AllowVersionMismatch: true,
	})
	defer s2.Close()

	// Read supervisor's hello, then send a different-version guest hello.
	go func() {
		_, _ = proto.Read(bufio.NewReader(p2))
		_ = proto.NewWriter(p2).Write(proto.THello, helloPayload)
	}()
	st, err := s2.Open(ctx, KindExec, map[string]string{"id": "e1"})
	if err != nil {
		t.Fatalf("Open with AllowVersionMismatch failed: %v", err)
	}
	_ = st.SetWriteDeadline(time.Now().Add(50 * time.Millisecond))
	_ = st.Close()
}

func TestStreamWaitPeerGoneDistinguishesCloseWriteFromClose(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	sup := New(c1, Supervisor, Options{Opener: BridgeOpener})
	defer sup.Close()
	cli := New(c2, Guest, Options{Opener: BridgeOpener})
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	supSt, err := sup.Open(ctx, KindExec, map[string]string{"id": "e1"})
	if err != nil {
		t.Fatal(err)
	}
	defer supSt.Close()
	cliSt, err := cli.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Peer CloseWrite (stdin EOF) must NOT wake WaitPeerGone or set PeerGone.
	if err := cliSt.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	shortCtx, shortCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer shortCancel()
	if supSt.WaitPeerGone(shortCtx) {
		t.Fatal("WaitPeerGone returned true on CloseWrite; expected false")
	}
	if supSt.PeerGone() {
		t.Fatal("PeerGone = true after CloseWrite; expected false")
	}

	// Full Close from peer MUST wake WaitPeerGone promptly.
	done := make(chan bool, 1)
	go func() {
		done <- supSt.WaitPeerGone(ctx)
	}()
	if err := cliSt.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case gone := <-done:
		if !gone {
			t.Fatal("WaitPeerGone returned false after peer Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitPeerGone did not wake after peer Close")
	}
	if !supSt.PeerGone() {
		t.Fatal("PeerGone = false after peer Close")
	}
}
