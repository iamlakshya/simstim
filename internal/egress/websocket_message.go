package egress

import (
	"errors"
	"fmt"
	"io"
	"sync/atomic"
)

type webSocketFramePayload struct {
	frame *webSocketFrame
	input io.Reader
}

func (r *webSocketFramePayload) Read(output []byte) (int, error) {
	return r.frame.read(r.input, output)
}

type webSocketMessageReader struct {
	direction  *webSocketDirection
	frame      *webSocketFrame
	declared   uint64
	ended      bool
	peerClosed bool
	failure    *webSocketFailure
	closed     atomic.Bool
	reading    atomic.Bool
}

func (r *webSocketMessageReader) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<WebSocket message redacted>")
}

func (r *webSocketMessageReader) recordFailure(err error) {
	var failure webSocketFailure
	if errors.As(err, &failure) {
		r.failure = &failure
	}
}

func (r *webSocketMessageReader) Read(output []byte) (n int, err error) {
	defer func() { r.recordFailure(err) }()
	if len(output) == 0 {
		return 0, nil
	}
	r.reading.Store(true)
	defer r.reading.Store(false)
	if r.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	for {
		if r.frame.remaining != 0 {
			return r.frame.read(r.direction.input, output)
		}
		if r.frame.final {
			r.ended = true
			return 0, io.EOF
		}
		frame, err := readWebSocketFrame(r.direction.input, r.direction.masked)
		if err != nil {
			return 0, webSocketFailure{1002}
		}
		if frame.opcode >= webSocketClose {
			if err := r.direction.control(frame); err != nil {
				r.peerClosed = errors.Is(err, errWebSocketPeerClosed)
				return 0, err
			}
			continue
		}
		if frame.opcode != 0 {
			return 0, webSocketFailure{1002}
		}
		if frame.remaining > maxDecodedHTTPBody-r.declared {
			return 0, webSocketFailure{1009}
		}
		r.declared += frame.remaining
		r.frame = frame
	}
}

func (r *webSocketMessageReader) Close() error {
	// This is the filter's owned message view, not the owned connection.
	// Closing an active Read closes the owned request origin first. It
	// never joins this worker. Closing after EOF leaves later messages alive.
	r.closed.Store(true)
	if r.reading.Load() {
		r.direction.owner.shutdown()
	}
	return nil
}

type webSocketFilterInput struct {
	io.Reader
	close   func() error
	onError func(error)
}

func (r *webSocketFilterInput) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<WebSocket filter input redacted>")
}
func (r *webSocketFilterInput) Read(output []byte) (int, error) {
	n, err := r.Reader.Read(output)
	if err != nil && r.onError != nil {
		r.onError(err)
	}
	return n, err
}
func (r *webSocketFilterInput) Close() error { return r.close() }
