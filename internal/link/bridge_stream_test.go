package link

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func TestBridgeOpenerAndVersionMismatch(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	sup := New(a, Supervisor, Options{
		Opener:               BridgeOpener,
		AllowVersionMismatch: true,
		MaxSupervisorStreams: 4,
		MaxGuestStreams:      1,
	})
	defer sup.Close()

	// Send a client hello with a different version to verify AllowVersionMismatch.
	bw := proto.NewWriter(b)
	if err := bw.WriteJSON(proto.THello, proto.Hello{
		Magic:    proto.Magic,
		Protocol: version.Protocol,
		Version:  "9.9.9-other",
		Role:     Guest.String(),
	}); err != nil {
		t.Fatalf("write mismatched-version hello: %v", err)
	}

	// Read supervisor's hello, then open a KindSFTP stream from Supervisor.
	f, err := proto.Read(bufio.NewReader(b))
	if err != nil || f.Type != proto.THello {
		t.Fatalf("read supervisor hello: %v, %v", f.Type, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := sup.Open(ctx, KindSFTP, nil)
	if err != nil {
		t.Fatalf("sup.Open(KindSFTP) with AllowVersionMismatch: %v", err)
	}
	if _, err := sup.Open(ctx, KindEvents, nil); err != nil {
		t.Fatalf("sup.Open(KindEvents): %v", err)
	}
	_ = st.Close()
}

func TestVersionMismatchStillRefusedByDefault(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	sup := New(a, Supervisor, Options{})
	defer sup.Close()

	bw := proto.NewWriter(b)
	_ = bw.WriteJSON(proto.THello, proto.Hello{
		Magic:    proto.Magic,
		Protocol: version.Protocol,
		Version:  "9.9.9-other",
		Role:     Guest.String(),
	})

	select {
	case <-sup.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("supervisor link did not close on version mismatch")
	}
	var ve *VersionError
	if !errors.As(sup.Err(), &ve) {
		t.Fatalf("sup.Err() = %v, want *VersionError", sup.Err())
	}
}

func TestPeerGoneDistinguishesHalfCloseAndFullClose(t *testing.T) {
	a, b := net.Pipe()
	sup := New(a, Supervisor, Options{Opener: BridgeOpener, AllowVersionMismatch: true})
	cli := New(b, Guest, Options{Opener: BridgeOpener, AllowVersionMismatch: true})
	defer sup.Close()
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	supSt, err := sup.Open(ctx, KindExec, map[string]string{"id": "e1"})
	if err != nil {
		t.Fatal(err)
	}
	cliSt, err := cli.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var hdr map[string]string
	if err := cliSt.Header(&hdr); err != nil || hdr["id"] != "e1" {
		t.Fatalf("hdr = %v, err = %v", hdr, err)
	}

	// Half-close from client: supervisor reads EOF, PeerGone stays false.
	if err := cliSt.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(supSt); err != nil {
		t.Fatal(err)
	}
	if supSt.PeerGone() {
		t.Fatal("PeerGone = true after CloseWrite, want false")
	}

	// Full close from client: PeerGone becomes true.
	if err := cliSt.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !supSt.PeerGone() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !supSt.PeerGone() {
		t.Fatal("PeerGone = false after client Close, want true")
	}
	_ = supSt.Close()
}
