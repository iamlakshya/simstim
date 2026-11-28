package guest

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/link"
)

func TestServePortRelaysLoopbackTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()

	supConn, guestConn := net.Pipe()
	runDir := t.TempDir()
	done := make(chan error, 1)
	go func() { done <- RunConn(guestConn, runDir) }()

	sess := link.New(supConn, link.Supervisor, link.Options{})
	t.Cleanup(func() {
		_ = sess.Close()
		<-done
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Valid port relays bytes cleanly both ways and propagates half-close.
	st, err := sess.Open(ctx, link.KindPort, PortHeader{Port: port})
	if err != nil {
		t.Fatalf("open port stream: %v", err)
	}
	want := "hello through guest port stream"
	if _, err := st.Write([]byte(want)); err != nil {
		t.Fatalf("write port stream: %v", err)
	}
	if err := st.CloseWrite(); err != nil {
		t.Fatalf("close-write port stream: %v", err)
	}
	got, err := io.ReadAll(st)
	if err != nil {
		t.Fatalf("read port stream: %v", err)
	}
	_ = st.Close()
	if string(got) != want {
		t.Fatalf("echoed %q, want %q", got, want)
	}

	// Out-of-range port closes cleanly without breaking the link.
	bad, err := sess.Open(ctx, link.KindPort, PortHeader{Port: 0})
	if err != nil {
		t.Fatalf("open invalid port stream: %v", err)
	}
	if b, _ := io.ReadAll(bad); len(b) != 0 {
		t.Fatalf("invalid port returned %q", b)
	}
	_ = bad.Close()
}
