package host

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

type prepareDeadlineCutV1 struct {
	data []byte
	cut  int
	at   int
	stop bool
}

func (reader *prepareDeadlineCutV1) Read(out []byte) (int, error) {
	if reader.stop && reader.at >= reader.cut {
		return 0, os.ErrDeadlineExceeded
	}
	limit := len(reader.data)
	if reader.stop {
		limit = min(limit, reader.cut)
	}
	if reader.at == len(reader.data) {
		return 0, io.EOF
	}
	n := copy(out, reader.data[reader.at:limit])
	reader.at += n
	return n, nil
}

func TestSandboxPrepareCursorResumesEveryOriginalFrameByteCut(t *testing.T) {
	payload := []byte(`{"id":3,"ok":true,"result":{"startup_nonce":"0123","generation":"4567"}}`)
	encoded, err := proto.EncodeFrame(proto.TResponse, payload)
	if err != nil {
		t.Fatal(err)
	}
	for cut := 0; cut < len(encoded); cut++ {
		reader := &prepareDeadlineCutV1{data: encoded, cut: cut, stop: true}
		cursor := &sandboxPrepareFrameCursorV1{active: true}
		if _, err := cursor.read(reader); !sandboxPrepareReadDeadlineV1(err) {
			t.Fatalf("cut %d did not preserve its original read deadline: %v", cut, err)
		}
		if reader.at != cut || cursor.headerN+cursor.bodyN != cut {
			t.Fatalf("cut %d replayed or discarded consumed bytes: reader %d cursor %d", cut, reader.at, cursor.headerN+cursor.bodyN)
		}
		reader.stop = false
		frame, err := cursor.read(reader)
		if err != nil || frame.Type != proto.TResponse || !bytes.Equal(frame.Payload, payload) || cursor.active {
			t.Fatalf("cut %d changed the resumed frame: %+v, %v", cut, frame, err)
		}
	}
}

func TestSandboxPrepareCursorRejectsInvalidLengthBeforeBodyAllocation(t *testing.T) {
	for _, length := range []uint32{0, proto.MaxFrame + 1, ^uint32(0)} {
		var header [4]byte
		binary.LittleEndian.PutUint32(header[:], length)
		cursor := &sandboxPrepareFrameCursorV1{active: true}
		_, err := cursor.read(bytes.NewReader(header[:]))
		if err == nil || cursor.body != nil {
			t.Fatalf("invalid declared length %d allocated or decoded a body: %v", length, err)
		}
	}
}

func TestSandboxPrepareCursorEOFNeverBecomesReadDeadline(t *testing.T) {
	encoded, err := proto.EncodeFrame(proto.TResponse, []byte(`{"id":4,"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	for cut := 0; cut < len(encoded); cut++ {
		cursor := &sandboxPrepareFrameCursorV1{active: true}
		_, err := cursor.read(bytes.NewReader(encoded[:cut]))
		if err == nil || sandboxPrepareReadDeadlineV1(err) || !(errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
			t.Fatalf("cut %d changed peer EOF into a resumable timeout: %v", cut, err)
		}
	}
}
