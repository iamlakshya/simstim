package host

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/iamlakshya/simstim/internal/proto"
)

// This cursor advances once over the canonical four-byte length header and
// bounded body on the SAME StartupLine reader. It never replays consumed bytes
// through a new decoder. Only a read deadline preserves partial state.
type sandboxPrepareFrameCursorV1 struct {
	header  [4]byte
	headerN int
	body    []byte
	bodyN   int
	active  bool
}

func sandboxPrepareReadDeadlineV1(err error) bool {
	var native net.Error
	return errors.As(err, &native) && native.Timeout()
}

func (cursor *sandboxPrepareFrameCursorV1) read(input io.Reader) (proto.Frame, error) {
	for cursor.headerN < len(cursor.header) {
		n, err := input.Read(cursor.header[cursor.headerN:])
		cursor.headerN += n
		if err != nil && !sandboxPrepareReadDeadlineV1(err) && !errors.Is(err, io.EOF) {
			return proto.Frame{}, err
		}
		if cursor.headerN < len(cursor.header) && err != nil {
			if errors.Is(err, io.EOF) && cursor.headerN != 0 {
				err = io.ErrUnexpectedEOF
			}
			return proto.Frame{}, fmt.Errorf("proto: read a frame: %w", err)
		}
	}
	if cursor.body == nil {
		length := binary.LittleEndian.Uint32(cursor.header[:])
		if length == 0 {
			return proto.Frame{}, errors.New("proto: empty frame")
		}
		if length > proto.MaxFrame {
			return proto.Frame{}, proto.ErrFrameTooBig
		}
		cursor.body = make([]byte, int(length))
	}
	for cursor.bodyN < len(cursor.body) {
		n, err := input.Read(cursor.body[cursor.bodyN:])
		cursor.bodyN += n
		if err != nil && !sandboxPrepareReadDeadlineV1(err) && !errors.Is(err, io.EOF) {
			return proto.Frame{}, err
		}
		if cursor.bodyN < len(cursor.body) && err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return proto.Frame{}, fmt.Errorf("proto: read a frame's body: %w", err)
		}
	}
	frame := proto.Frame{Type: proto.Type(cursor.body[0]), Payload: cursor.body[1:]}
	*cursor = sandboxPrepareFrameCursorV1{}
	return frame, nil
}
