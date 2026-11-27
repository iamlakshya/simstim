package link

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func guestHelloWire(t *testing.T, hello proto.Hello) []byte {
	t.Helper()
	var wire bytes.Buffer
	if err := proto.NewWriter(&wire).WriteJSON(proto.THello, hello); err != nil {
		t.Fatal(err)
	}
	return wire.Bytes()
}

func currentGuestHello() proto.Hello {
	return proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "guest"}
}

func awaitGuestHelloWorker(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("owned guest-hello worker did not join")
	}
}

func ownGuestHelloWriter(t *testing.T, conn net.Conn, wire []byte) (<-chan struct{}, *error) {
	t.Helper()
	done := make(chan struct{})
	var result error
	go func() {
		defer close(done)
		_, result = io.Copy(conn, bytes.NewReader(wire))
	}()
	t.Cleanup(func() { conn.Close(); awaitGuestHelloWorker(t, done) })
	return done, &result
}

func TestReadGuestHelloReplaysExactHelloAndPrefetchedFollowingBytes(t *testing.T) {
	local, remote := net.Pipe()
	t.Cleanup(func() { local.Close(); remote.Close() })
	wire := guestHelloWire(t, currentGuestHello())
	var tail bytes.Buffer
	if err := proto.NewWriter(&tail).Write(proto.TStreamData, []byte("following bytes")); err != nil {
		t.Fatal(err)
	}
	wire = append(wire, tail.Bytes()...)
	done, writeErr := ownGuestHelloWriter(t, remote, wire)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	checked, err := ReadGuestHello(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	actual := make([]byte, len(wire))
	if _, err := io.ReadFull(checked, actual); err != nil || !bytes.Equal(actual, wire) {
		t.Fatalf("checked stream lost, duplicated or rewrote bytes: %v", err)
	}
	awaitGuestHelloWorker(t, done)
	if *writeErr != nil {
		t.Fatal(*writeErr)
	}
}

type guestHelloOuterConn struct{ net.Conn }

func TestReadGuestHelloUsesNormalNewThroughAnActualConnectionWrapper(t *testing.T) {
	local, remote := net.Pipe()
	guest := New(remote, Guest, Options{})
	t.Cleanup(func() { guest.Close(); local.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	checked, err := ReadGuestHello(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	// Real disk/connection owners may wrap net.Conn between Start and New.
	// No out-of-band adopted flag may be lost at that boundary.
	host := New(guestHelloOuterConn{checked}, Supervisor, Options{})
	t.Cleanup(func() { host.Close() })
	out, err := host.Open(ctx, KindCtl, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	in, err := guest.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("same connection")
	if _, err := out.Write(message); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(message))
	if _, err := io.ReadFull(in, got); err != nil || !bytes.Equal(got, message) {
		t.Fatalf("normal link continuation changed after checked hello: %v", err)
	}
}

func TestReadGuestHelloRetainsCanonicalRoleAndVersionRefusals(t *testing.T) {
	for _, kind := range []string{"role", "version"} {
		t.Run(kind, func(t *testing.T) {
			local, remote := net.Pipe()
			t.Cleanup(func() { local.Close(); remote.Close() })
			hello := currentGuestHello()
			if kind == "role" {
				hello.Role = "supervisor"
			} else {
				hello.Version = "incompatible-test-version"
			}
			ownGuestHelloWriter(t, remote, guestHelloWire(t, hello))
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			checked, err := ReadGuestHello(ctx, local)
			if err == nil || checked != nil {
				t.Fatal("a rejected canonical hello supplied a checked connection")
			}
			var versionError *VersionError
			if kind == "version" && !errors.As(err, &versionError) || kind == "role" && !errors.Is(err, ErrBreach) {
				t.Fatalf("canonical typed refusal changed: %v", err)
			}
		})
	}
}

type guestHelloReadObserver struct {
	net.Conn
	reading chan struct{}
	once    sync.Once
}

func (conn *guestHelloReadObserver) Read(data []byte) (int, error) {
	conn.once.Do(func() { close(conn.reading) })
	return conn.Conn.Read(data)
}

func TestReadGuestHelloCancellationJoinsDeadlineCallbackAndKeepsCallerFD(t *testing.T) {
	local, remote := net.Pipe()
	observed := &guestHelloReadObserver{Conn: local, reading: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var checked net.Conn
	var failure error
	go func() { defer close(done); checked, failure = ReadGuestHello(ctx, observed) }()
	t.Cleanup(func() {
		cancel()
		local.Close()
		remote.Close()
		awaitGuestHelloWorker(t, done)
	})
	awaitGuestHelloWorker(t, observed.reading)
	cancel()
	awaitGuestHelloWorker(t, done)
	if checked != nil || !errors.Is(failure, context.Canceled) {
		t.Fatalf("cancelled hello supplied success: %v", failure)
	}
	writer, writeErr := ownGuestHelloWriter(t, remote, []byte("open"))
	got := make([]byte, 4)
	if _, err := io.ReadFull(local, got); err != nil || string(got) != "open" {
		t.Fatal("checker closed caller FD or left its cancelled deadline installed", err)
	}
	awaitGuestHelloWorker(t, writer)
	if *writeErr != nil {
		t.Fatal(*writeErr)
	}
}
