package link

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

const EditorTerminalMaxV1 = 32 << 10

// EditorTerminalV1 carries one ephemeral terminal on its already owned stream.
// Frames contain bytes or bounded controls, never a target PID or host path.
type EditorTerminalV1 struct {
	stream io.ReadWriteCloser
	writer *proto.Writer
	mu     sync.Mutex
	ended  bool
}

func NewEditorTerminalV1(stream io.ReadWriteCloser) *EditorTerminalV1 {
	return &EditorTerminalV1{stream: stream, writer: proto.NewWriter(stream)}
}

func (p *EditorTerminalV1) WriteData(kind proto.Type, data []byte) (int, error) {
	if kind != proto.TInput && kind != proto.TOutput {
		return 0, editorTerminalError("invalid byte direction")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended {
		return 0, io.ErrClosedPipe
	}
	written := 0
	for len(data) != 0 {
		n := min(len(data), EditorTerminalMaxV1)
		if err := p.writer.Write(kind, data[:n]); err != nil {
			return written, err
		}
		written += n
		data = data[n:]
	}
	return written, nil
}

func (p *EditorTerminalV1) Control(kind proto.Type, value any) error {
	if kind != proto.TResize && kind != proto.TRequest && kind != proto.TEnded {
		return editorTerminalError("invalid control direction")
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > 1024 {
		return editorTerminalError("oversized control")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ended {
		return io.ErrClosedPipe
	}
	if kind == proto.TEnded {
		p.ended = true
	}
	return p.writer.Write(kind, data)
}

func (p *EditorTerminalV1) Read() (proto.Frame, error) {
	return ReadEditorFrameV1(p.stream, EditorTerminalMaxV1+1)
}

// ReadEditorFrameV1 never reads ahead of this frame. The ready handoff may
// therefore switch to raw SFTP bytes without losing buffered protocol data.
func ReadEditorFrameV1(reader io.Reader, limit uint32) (proto.Frame, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return proto.Frame{}, err
	}
	size := binary.LittleEndian.Uint32(header[:])
	if size == 0 || size > limit || size > proto.MaxFrame {
		return proto.Frame{}, editorTerminalError("oversized or empty frame")
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(reader, data); err != nil {
		return proto.Frame{}, err
	}
	return proto.Frame{Type: proto.Type(data[0]), Payload: data[1:]}, nil
}

func DecodeEditorControlV1(data []byte, value any) error {
	if len(data) > 1024 || !api.ExactJSONFields(data, value) || json.Unmarshal(data, value) != nil {
		return editorTerminalError("invalid closed control")
	}
	return nil
}

func editorTerminalError(message string) error {
	return fmt.Errorf("ephemeral terminal: %s. Try: reconnect with matching client and helper versions", message)
}
