//go:build !windows

package e2b

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestFakeOutCutKeepsQueuedBytes(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	o := newFakeOut(r)
	t.Cleanup(func() { _ = w.Close(); _ = o.Close() })
	want := bytes.Repeat([]byte("first\nsecond\n"), 64)
	if _, err := w.Write(want); err != nil {
		t.Fatal(err)
	}
	o.cut()
	o.cut()
	var got []byte
	var b [7]byte
	for {
		n, err := o.Read(b[:])
		got = append(got, b[:n]...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read %q, want %q", got, want)
	}
}

func TestFakeOutCutWakesAnActiveRead(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	o := newFakeOut(r)
	t.Cleanup(func() { _ = w.Close(); _ = o.Close() })
	read := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := o.Read(b[:])
		read <- err
	}()
	waitFor(t, "the active output read", func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		return o.reading
	})
	cut := make(chan struct{})
	go func() { o.cut(); close(cut) }()
	select {
	case <-cut:
	case <-time.After(5 * time.Second):
		t.Fatal("cut waited for the open writer")
	}
	select {
	case err := <-read:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Read = %v, want EOF without the internal deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read did not end after cut")
	}
}

func TestDrainPipeStopsAtItsCap(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	if _, err := w.Write([]byte("after the boundary")); err != nil {
		t.Fatal(err)
	}
	const capBytes = 1 << 20
	got := bytes.Repeat([]byte("x"), capBytes-1)
	drainPipe(r, &got)
	if len(got) != capBytes || got[capBytes-1] != 'a' {
		t.Fatalf("drained length %d, want %d with the next byte", len(got), capBytes)
	}
}
