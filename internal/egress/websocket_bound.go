package egress

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// A filter receives one decoded logical payload, not an individual wire frame.
// It must close promptly on context cancellation and must not close or join the
// direction worker through input.Close. Server uses the canonical response
// credential engine. Client is the canonical readonly sentinel checker. Client
// output MUST be byte-identical. No real credential is injected into a message.
type webSocketMessageFilter func(context.Context, byte, io.ReadCloser) (io.ReadCloser, error)

type webSocketFilters struct {
	Server webSocketMessageFilter
	Client webSocketMessageFilter
}

// The facade holds no value-bearing mutable fields. Formatting never traverses
// owned origin IO, queued decoded bytes or filter closures.
type boundWebSocket struct{ state *boundWebSocketState }

func (b boundWebSocket) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<bound WebSocket redacted>")
}

type boundWebSocketState struct {
	ctx            context.Context
	cancel         context.CancelFunc
	origin         io.ReadWriteCloser
	input          *io.PipeReader
	upload         *io.PipeWriter
	output         chan []byte
	done           chan struct{}
	workers        atomic.Int32
	originOnce     sync.Once
	shutdownOnce   sync.Once
	closeOnce      sync.Once
	stopParent     func() bool
	parentDone     chan struct{}
	parentFinished func()
	readMu         sync.Mutex
	writeMu        sync.Mutex
	mu             sync.Mutex
	failure        error
	failureFrame   []byte
	closed         bool
	pending        []byte
}

func (b *boundWebSocketState) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<bound WebSocket state redacted>")
}

// Constructor failure leaves origin with the caller. Success transfers it. The
// actual origin must make Close promptly unblock both Read and Write. Close of
// this wrapper closes origin FIRST, then cancels filters and joins both workers.
func newBoundWebSocket(ctx context.Context, origin io.ReadWriteCloser, filters webSocketFilters) (io.ReadWriteCloser, error) {
	if ctx == nil || origin == nil || filters.Server == nil || filters.Client == nil {
		return nil, errors.New("missing bound WebSocket owner or filter. Try: prepare both credential directions before upgrading")
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.New("bound WebSocket request was cancelled. Try: open a new allowed request")
	}
	workerContext, cancel := context.WithCancel(context.WithoutCancel(ctx))
	input, upload := io.Pipe()
	s := &boundWebSocketState{ctx: workerContext, cancel: cancel, origin: origin, input: input, upload: upload,
		output: make(chan []byte, 2), done: make(chan struct{}), parentDone: make(chan struct{})}
	s.parentFinished = sync.OnceFunc(func() { close(s.parentDone) })
	s.stopParent = context.AfterFunc(ctx, func() {
		defer s.parentFinished()
		s.shutdown()
	})
	server := &webSocketDirection{owner: s, input: origin, filter: filters.Server, emit: s.emitGuest}
	client := &webSocketDirection{owner: s, input: input, filter: filters.Client, masked: true, emit: s.emitOrigin, emitRequest: s.emitOriginRequest}
	go s.runDirection(server)
	go s.runDirection(client)
	return boundWebSocket{state: s}, nil
}

func (s *boundWebSocketState) runDirection(direction *webSocketDirection) {
	defer func() {
		if direction.masked {
			_ = s.input.CloseWithError(io.ErrClosedPipe)
		}
		if s.workers.Add(1) == 2 {
			s.closeOrigin()
			close(s.output)
			close(s.done)
		}
	}()
	if err := direction.run(); err != nil && !errors.Is(err, errWebSocketPeerClosed) && s.ctx.Err() == nil {
		s.fail(err)
	}
}

func (s *boundWebSocketState) closeOrigin() {
	s.originOnce.Do(func() { _ = s.origin.Close() })
}

func (s *boundWebSocketState) shutdown() {
	s.shutdownOnce.Do(func() {
		// Closing the real connection must precede filter cancellation. A
		// decoder/input Close may itself wait for its blocked origin read.
		s.closeOrigin()
		_ = s.upload.CloseWithError(io.ErrClosedPipe)
		_ = s.input.CloseWithError(io.ErrClosedPipe)
		s.cancel()
	})
}

func (s *boundWebSocketState) fail(err error) {
	failure := webSocketFailure{1008}
	var protocol webSocketFailure
	if errors.As(err, &protocol) {
		failure = protocol
	}
	// The reason is static and contains no input, value or callback error.
	payload := make([]byte, 2, 125)
	binary.BigEndian.PutUint16(payload, failure.code)
	reason := failure.Error()
	var model modelWebSocketRefusal
	if errors.As(err, &model) {
		reason = model.line
	}
	if len(reason) > 123 {
		reason = reason[:123]
		for !utf8.ValidString(reason) {
			reason = reason[:len(reason)-1]
		}
	}
	payload = append(payload, reason...)
	wire, _ := encodeWebSocketFrame(webSocketClose, true, false, payload, nil)
	clear(payload)
	s.mu.Lock()
	if s.failure == nil && !s.closed {
		s.failure, s.failureFrame = failure, wire
	} else {
		clear(wire)
	}
	s.mu.Unlock()
	s.shutdown()
}

func (s *boundWebSocketState) emitGuest(wire []byte) error {
	select {
	case s.output <- wire:
		return nil
	case <-s.ctx.Done():
		clear(wire)
		return s.ctx.Err()
	}
}

func (s *boundWebSocketState) emitOrigin(wire []byte) error {
	return s.emitOriginRequest(wire, nil, len(wire))
}

func (s *boundWebSocketState) emitOriginRequest(wire []byte, turn *ModelUsageTurnV1, payloadStart int) error {
	defer clear(wire)
	position := 0
	for len(wire) != 0 {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		n, err := s.origin.Write(wire)
		if turn != nil && n > 0 {
			body := max(0, position+n-max(position, payloadStart))
			if body > 0 {
				turn.physicalRequestPrefixV1(uint64(body))
			}
		}
		position += n
		wire = wire[n:]
		if err != nil {
			return webSocketFailure{1002}
		}
		if n == 0 {
			return webSocketFailure{1002}
		}
	}
	return nil
}

func (b boundWebSocket) Read(output []byte) (int, error) {
	if len(output) == 0 {
		return 0, nil
	}
	s := b.state
	s.readMu.Lock()
	defer s.readMu.Unlock()
	for {
		// Finish an already accepted frame before a terminal close frame.
		// Inserting a close header into that frame's payload would be invalid.
		if len(s.pending) != 0 {
			n := copy(output, s.pending)
			clear(s.pending[:n])
			s.pending = s.pending[n:]
			return n, nil
		}
		if s.ctx.Err() != nil {
			s.mu.Lock()
			wire, failure, closed := s.failureFrame, s.failure, s.closed
			s.failureFrame = nil
			s.mu.Unlock()
			if !closed && len(wire) != 0 {
				s.pending = wire
				continue
			}
			if failure != nil {
				return 0, failure
			}
			return 0, io.ErrClosedPipe
		}
		select {
		case wire, ok := <-s.output:
			if !ok {
				if s.ctx.Err() != nil {
					continue
				}
				return 0, io.EOF
			}
			if s.ctx.Err() != nil {
				clear(wire)
				continue
			}
			s.pending = wire
		case <-s.ctx.Done():
		}
	}
}

func (b boundWebSocket) Write(input []byte) (int, error) {
	s := b.state
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.ctx.Err() != nil {
		return 0, io.ErrClosedPipe
	}
	return s.upload.Write(input)
}

func (b boundWebSocket) Close() error {
	s := b.state
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.shutdown()
		if s.stopParent() {
			s.parentFinished()
		}
		<-s.parentDone
		<-s.done
		s.readMu.Lock()
		clear(s.pending)
		s.pending = nil
		for wire := range s.output {
			clear(wire)
		}
		s.readMu.Unlock()
		s.mu.Lock()
		clear(s.failureFrame)
		s.failureFrame = nil
		s.origin = nil
		s.mu.Unlock()
	})
	return nil
}

type webSocketDirection struct {
	owner       *boundWebSocketState
	input       io.Reader
	filter      webSocketMessageFilter
	masked      bool
	emit        func([]byte) error
	emitRequest func([]byte, *ModelUsageTurnV1, int) error
	requestTurn *ModelUsageTurnV1
}

func (d *webSocketDirection) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<WebSocket direction redacted>")
}

func (d *webSocketDirection) run() error {
	for {
		frame, err := readWebSocketFrame(d.input, d.masked)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return webSocketFailure{1002} // transport EOF is not a close handshake
			}
			return err
		}
		if frame.opcode >= webSocketClose {
			if err := d.control(frame); err != nil {
				return err
			}
			continue
		}
		if frame.opcode == 0 {
			return webSocketFailure{1002}
		}
		if frame.remaining > maxDecodedHTTPBody {
			return webSocketFailure{1009}
		}
		message := &webSocketMessageReader{direction: d, frame: frame, declared: frame.remaining}
		if err := d.message(frame.opcode, message); err != nil {
			return err
		}
	}
}

func (d *webSocketDirection) message(opcode byte, message *webSocketMessageReader) error {
	defer message.Close()
	var input io.ReadCloser = message
	if opcode == 1 {
		text := newWebSocketTextReader(message)
		defer text.clear()
		input = &webSocketFilterInput{Reader: text, close: message.Close, onError: message.recordFailure}
	}
	filtered, err := d.filter(d.owner.ctx, opcode, input)
	if err != nil || filtered == nil {
		var model modelWebSocketRefusal
		if errors.As(err, &model) {
			return model
		}
		return webSocketFailure{1008}
	}
	if d.masked {
		if request, ok := filtered.(interface {
			physicalModelRequestV1() *ModelUsageTurnV1
			physicalModelRequestFinishedV1()
		}); ok {
			d.requestTurn = request.physicalModelRequestV1()
			defer func() { request.physicalModelRequestFinishedV1(); d.requestTurn = nil }()
		}
	}
	closeFiltered := sync.OnceValue(filtered.Close)
	defer closeFiltered()
	var output io.Reader = filtered
	if opcode == 1 {
		text := newWebSocketTextReader(filtered)
		defer text.clear()
		output = text
	}
	var buffer [webSocketChunk]byte
	defer clear(buffer[:])
	first, written, emptyReads := true, uint64(0), 0
	for {
		n, readErr := output.Read(buffer[:])
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			if message.peerClosed || errors.Is(readErr, errWebSocketPeerClosed) {
				return errWebSocketPeerClosed
			}
			if message.failure != nil {
				return *message.failure
			}
			var failure webSocketFailure
			if errors.As(readErr, &failure) {
				return failure
			}
			return webSocketFailure{1008}
		}
		if errors.Is(readErr, io.EOF) {
			// Validate the filter's claimed EOF before releasing its final
			// returned bytes. A frame boundary is never a message boundary.
			if !message.ended {
				return webSocketFailure{1008}
			}
			if err := closeFiltered(); err != nil {
				return webSocketFailure{1008}
			}
		}
		if written+uint64(n) > maxDecodedHTTPBody {
			return webSocketFailure{1009}
		}
		written += uint64(n)
		if n != 0 {
			outOpcode := byte(0)
			if first {
				outOpcode = opcode
			}
			if err := d.send(outOpcode, false, buffer[:n]); err != nil {
				return err
			}
			first, emptyReads = false, 0
		}
		if errors.Is(readErr, io.EOF) {
			outOpcode := byte(0)
			if first {
				outOpcode = opcode
			}
			return d.send(outOpcode, true, nil)
		}
		if n == 0 {
			emptyReads++
			if emptyReads >= 100 {
				return webSocketFailure{1008}
			}
		}
	}
}

func (d *webSocketDirection) send(opcode byte, final bool, payload []byte) error {
	wire, err := encodeWebSocketFrame(opcode, final, d.masked, payload, rand.Reader)
	if err != nil {
		return err
	}
	if d.masked && opcode < webSocketClose && d.requestTurn != nil && d.emitRequest != nil {
		return d.emitRequest(wire, d.requestTurn, len(wire)-len(payload))
	}
	return d.emit(wire)
}

func (d *webSocketDirection) control(frame *webSocketFrame) error {
	var raw [125]byte
	defer clear(raw[:])
	n := int(frame.remaining)
	if _, err := io.ReadFull(&webSocketFramePayload{frame: frame, input: d.input}, raw[:n]); err != nil {
		return webSocketFailure{1002}
	}
	if frame.opcode == webSocketClose {
		if err := validateWebSocketClose(raw[:n], d.masked); err != nil {
			return err
		}
	}
	prefix := 0
	if frame.opcode == webSocketClose && n != 0 {
		prefix = 2 // a wire status is structural, not part of the reason
	}
	source := bytes.NewReader(raw[prefix:n])
	input := io.NopCloser(source)
	filtered, err := d.filter(d.owner.ctx, frame.opcode, input)
	if err != nil || filtered == nil {
		return webSocketFailure{1008}
	}
	// RFC control payloads are intrinsically bounded at 125 bytes. The
	// extra probe rejects a faulty expanding filter without changing that.
	var safe [126]byte
	defer clear(safe[:])
	copy(safe[:], raw[:prefix])
	count, readErr := io.ReadFull(filtered, safe[prefix:])
	closeErr := filtered.Close()
	if source.Len() != 0 || closeErr != nil || readErr != io.EOF && readErr != io.ErrUnexpectedEOF || count+prefix > 125 {
		return webSocketFailure{1008}
	}
	safeLength := prefix + count
	if frame.opcode == webSocketClose {
		if err := validateWebSocketClose(safe[:safeLength], d.masked); err != nil {
			return err
		}
	}
	if err := d.send(frame.opcode, true, safe[:safeLength]); err != nil {
		return err
	}
	if frame.opcode == webSocketClose {
		return errWebSocketPeerClosed
	}
	return nil
}
