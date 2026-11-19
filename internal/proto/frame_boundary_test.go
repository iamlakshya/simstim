package proto

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

func TestFrameMaximumPayloadHasExactCanonicalBoundary(t *testing.T) {
	payload := bytes.Repeat([]byte{0x9b}, MaxFrame-1)
	encoded, err := frame(TOutput, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != MaxFrame+4 || binary.LittleEndian.Uint32(encoded[:4]) != uint32(MaxFrame) || encoded[4] != byte(TOutput) || !bytes.Equal(encoded[5:], payload) {
		t.Fatal("maximum frame changed its header, type or payload bytes")
	}
	decoded, err := Read(bufio.NewReader(bytes.NewReader(encoded)))
	if err != nil || decoded.Type != TOutput || !bytes.Equal(decoded.Payload, payload) {
		t.Fatal("maximum legal frame did not round trip exactly", err)
	}
	// Zero payload still carries its single type byte and canonical length.
	empty, err := frame(TOutput, nil)
	if err != nil || !bytes.Equal(empty, []byte{1, 0, 0, 0, byte(TOutput)}) {
		t.Fatal("empty payload changed its canonical frame", err)
	}
}

type frameBoundaryWrites struct{ calls int }

func (writer *frameBoundaryWrites) Write(data []byte) (int, error) {
	writer.calls++
	return len(data), nil
}

func TestFrameOversizeRejectsBeforeAnyTransportWrite(t *testing.T) {
	for _, length := range []int{MaxFrame, MaxFrame + 1} {
		payload := make([]byte, length)
		encoded, err := frame(TOutput, payload)
		if !errors.Is(err, ErrFrameTooBig) || encoded != nil {
			t.Fatal("oversized frame was materialised", length, err)
		}
		output := &frameBoundaryWrites{}
		if err := NewWriter(output).Write(TOutput, payload); !errors.Is(err, ErrFrameTooBig) || output.calls != 0 {
			t.Fatal("oversized payload touched its transport", length, output.calls, err)
		}
	}
}
