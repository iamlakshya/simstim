package host

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestBridgeAttachPassesExactFramesAndOldReaderKeepsReplacement(t *testing.T) {
	old, peer := net.Pipe()
	replacement, nextPeer := net.Pipe()
	for _, conn := range []net.Conn{old, peer, replacement, nextPeer} {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		t.Cleanup(func() { _ = conn.Close() })
	}
	var output bytes.Buffer
	b := &bridge{holder: old, hw: proto.NewWriter(old), w: proto.NewWriter(&output)}
	done := make(chan struct{})
	go func() { defer close(done); b.readHolder(old, bufio.NewReader(old)) }()
	t.Cleanup(func() {
		_ = peer.Close()
		b.endAttach()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("owned holder reader did not finish")
		}
	})
	terminal := []byte{'x', 0, '\r', '\n', '\x1b', '[', '3', '1', 'm', 0xff}
	frames := []proto.Frame{{Type: proto.TOutput, Payload: proto.EncodeOutput(17, terminal)}, {Type: proto.TSnapshot, Payload: terminal}, {Type: proto.TAttached, Payload: []byte(`{"ok":true}`)}}
	writer := proto.NewWriter(peer)
	for _, frame := range frames {
		if err := writer.Write(frame.Type, frame.Payload); err != nil {
			t.Fatal(err)
		}
	}
	nextWriter := proto.NewWriter(replacement)
	b.mu.Lock()
	b.holder, b.hw = replacement, nextWriter
	b.mu.Unlock()
	_ = peer.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("old reader did not join")
	}
	reader := bufio.NewReader(&output)
	for _, want := range frames {
		got, err := proto.Read(reader)
		if err != nil || got.Type != want.Type || !bytes.Equal(got.Payload, want.Payload) {
			t.Fatal("the attached bridge changed a frame", got, err)
		}
	}
	if _, err := proto.Read(reader); !errors.Is(err, io.EOF) {
		t.Fatal("old reader reported the replacement as stopped", err)
	}
	b.mu.Lock()
	kept := b.holder == replacement && b.hw == nextWriter
	b.mu.Unlock()
	if !kept {
		t.Fatal("old reader detached a replacement connection")
	}
	forwarded := make(chan struct{})
	go func() { defer close(forwarded); b.forward(proto.Frame{Type: proto.TInput, Payload: terminal}) }()
	got, err := proto.Read(bufio.NewReader(nextPeer))
	if err != nil || got.Type != proto.TInput || !bytes.Equal(got.Payload, terminal) {
		t.Fatal("replacement input bytes changed or its connection closed", got, err)
	}
	<-forwarded
}

func TestEndedBridgeRefusesAttachBeforeOpeningAHolder(t *testing.T) {
	b := &bridge{}
	b.endAttach()
	if err := b.attach(proto.Attach{Session: "abcd2345"}); err == nil {
		t.Fatal("an ended bridge admitted another holder connection")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.holder != nil || b.hw != nil {
		t.Fatal("an ended bridge retained a holder")
	}
}
