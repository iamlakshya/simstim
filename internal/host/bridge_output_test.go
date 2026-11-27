package host

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestBridgeOutputACKFollowsExactBytes(t *testing.T) {
	wanted := []byte{'a', 0, '\r', '\n', '\x1b', '[', '3', '1', 'm', 'z'}
	var encoded, raw, acks bytes.Buffer
	if err := proto.NewWriter(&encoded).Write(proto.TOutput, wanted); err != nil {
		t.Fatal(err)
	}
	if err := RunBridgeOutput(&encoded, &acks, &raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw.Bytes(), wanted) {
		t.Fatal("the output helper changed terminal bytes")
	}
	frame, err := proto.Read(bufio.NewReader(&acks))
	if err != nil || frame.Type != proto.TPong || len(frame.Payload) != 0 {
		t.Fatalf("invalid completion ACK: %+v, %v", frame, err)
	}
}

func TestBridgeOutputFailureEmitsNoACK(t *testing.T) {
	for _, out := range []io.Writer{bridgeZeroWriter{}, bridgeErrorWriter{}} {
		var encoded, acks bytes.Buffer
		_ = proto.NewWriter(&encoded).Write(proto.TOutput, []byte("payload"))
		if err := RunBridgeOutput(&encoded, &acks, out); err == nil || acks.Len() != 0 {
			t.Fatal("a failed/partial raw write was acknowledged")
		}
	}
	var encoded, acks, raw bytes.Buffer
	_ = proto.NewWriter(&encoded).Write(proto.TInput, []byte("wrong role"))
	if err := RunBridgeOutput(&encoded, &acks, &raw); err == nil || raw.Len() != 0 || acks.Len() != 0 {
		t.Fatal("the closed helper accepted another role")
	}
}

type bridgeZeroWriter struct{}

func (bridgeZeroWriter) Write([]byte) (int, error) { return 0, nil }

type bridgeErrorWriter struct{}

func (bridgeErrorWriter) Write(p []byte) (int, error) {
	return len(p) / 2, errors.New("owned output stopped")
}
