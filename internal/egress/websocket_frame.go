package egress

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	webSocketChunk = 32 << 10
	webSocketClose = byte(8)
	webSocketPing  = byte(9)
	webSocketPong  = byte(10)
)

type webSocketFailure struct{ code uint16 }

func (e webSocketFailure) Error() string {
	switch e.code {
	case 1007:
		return "invalid WebSocket text. Try: send complete UTF-8 text messages"
	case 1009:
		return "WebSocket message exceeds 64 MiB. Try: reduce the decoded message size"
	case 1008:
		return "WebSocket message failed its credential check. Try: check the session's secret bindings"
	default:
		return "invalid or incomplete WebSocket framing. Try: use an uncompressed RFC 6455 connection"
	}
}

var errWebSocketPeerClosed = errors.New("WebSocket close received")

type webSocketFrame struct {
	opcode    byte
	final     bool
	remaining uint64
	position  uint64
	mask      [4]byte
	masked    bool
}

// RFC 6455 sections 5.2 through 5.5. All reserved bits are zero because this
// endpoint explicitly negotiates no extensions. Lengths are checked before
// allocating or reading payload bytes.
func readWebSocketFrame(input io.Reader, masked bool) (*webSocketFrame, error) {
	var header [2]byte
	if _, err := io.ReadFull(input, header[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, webSocketFailure{1002}
	}
	frame := &webSocketFrame{opcode: header[0] & 15, final: header[0]&128 != 0, masked: header[1]&128 != 0}
	if header[0]&112 != 0 || frame.masked != masked {
		return nil, webSocketFailure{1002}
	}
	if frame.opcode != 0 && frame.opcode != 1 && frame.opcode != 2 && frame.opcode != webSocketClose && frame.opcode != webSocketPing && frame.opcode != webSocketPong {
		return nil, webSocketFailure{1002}
	}
	length := header[1] & 127
	frame.remaining = uint64(length)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(input, extended[:]); err != nil {
			return nil, webSocketFailure{1002}
		}
		frame.remaining = uint64(binary.BigEndian.Uint16(extended[:]))
		if frame.remaining < 126 {
			return nil, webSocketFailure{1002}
		}
	case 127:
		var extended [8]byte
		if _, err := io.ReadFull(input, extended[:]); err != nil {
			return nil, webSocketFailure{1002}
		}
		frame.remaining = binary.BigEndian.Uint64(extended[:])
		if extended[0]&128 != 0 || frame.remaining < 65536 {
			return nil, webSocketFailure{1002}
		}
	}
	if frame.opcode >= webSocketClose && (!frame.final || frame.remaining > 125) {
		return nil, webSocketFailure{1002}
	}
	if frame.masked {
		if _, err := io.ReadFull(input, frame.mask[:]); err != nil {
			return nil, webSocketFailure{1002}
		}
	}
	return frame, nil
}

func (f *webSocketFrame) read(input io.Reader, buffer []byte) (int, error) {
	if f.remaining == 0 {
		return 0, io.EOF
	}
	if uint64(len(buffer)) > f.remaining {
		buffer = buffer[:f.remaining]
	}
	n, err := input.Read(buffer)
	if f.masked {
		for i := range n {
			buffer[i] ^= f.mask[(f.position+uint64(i))%4]
		}
	}
	f.position += uint64(n)
	f.remaining -= uint64(n)
	if err != nil {
		if f.remaining != 0 || !errors.Is(err, io.EOF) {
			return n, webSocketFailure{1002}
		}
		// A complete frame is not a logical message EOF. The message reader
		// separately interprets FIN and any following continuation frames.
		return n, nil
	}
	return n, nil
}

// encodeWebSocketFrame copies at most one bounded output chunk. The caller
// supplies crypto/rand.Reader for each client mask. No input storage is changed.
func encodeWebSocketFrame(opcode byte, final, masked bool, payload []byte, randomness io.Reader) ([]byte, error) {
	if len(payload) > webSocketChunk || opcode >= webSocketClose && len(payload) > 125 {
		return nil, webSocketFailure{1002}
	}
	var header [14]byte
	header[0] = opcode
	if final {
		header[0] |= 128
	}
	headerLength := 2
	if len(payload) <= 125 {
		header[1] = byte(len(payload))
	} else {
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:4], uint16(len(payload)))
		headerLength = 4
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(randomness, mask[:]); err != nil {
			return nil, webSocketFailure{1002}
		}
		header[1] |= 128
		copy(header[headerLength:], mask[:])
		headerLength += len(mask)
	}
	wire := make([]byte, headerLength+len(payload))
	copy(wire, header[:headerLength])
	copy(wire[headerLength:], payload)
	if masked {
		for i := range len(payload) {
			wire[headerLength+i] ^= mask[i%4]
		}
	}
	return wire, nil
}

func validateWebSocketClose(payload []byte, fromClient bool) error {
	if len(payload) == 0 {
		return nil
	}
	if len(payload) == 1 {
		return webSocketFailure{1002}
	}
	code := binary.BigEndian.Uint16(payload[:2])
	// 1004, 1005, 1006 and 1015 are not wire statuses. 1016 through 2999
	// require a future protocol/extension and are not negotiated here.
	if code < 1000 || code >= 1016 && code < 3000 || code > 4999 || code == 1004 || code == 1005 || code == 1006 || code == 1015 || code == 1010 && !fromClient {
		return webSocketFailure{1002}
	}
	if !utf8.Valid(payload[2:]) {
		return webSocketFailure{1007}
	}
	return nil
}

// webSocketTextReader releases complete, valid UTF-8 only. It retains at most
// three incomplete bytes across transport and continuation-frame boundaries.
// Its bounded scratch is cleared by the owning message worker.
type webSocketTextReader struct{ state *webSocketTextState }

type webSocketTextState struct {
	input       io.Reader
	buffer      [webSocketChunk + utf8.UTFMax]byte
	start, end  int
	tail        [utf8.UTFMax]byte
	tailLength  int
	pending     error
	terminalErr error
}

func newWebSocketTextReader(input io.Reader) *webSocketTextReader {
	return &webSocketTextReader{state: &webSocketTextState{input: input}}
}

func (r webSocketTextReader) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<WebSocket text redacted>")
}
func (r *webSocketTextState) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<WebSocket text state redacted>")
}

func (r *webSocketTextReader) Read(output []byte) (int, error) {
	if len(output) == 0 {
		return 0, nil
	}
	for noProgress := 0; ; noProgress++ {
		if r.state.start < r.state.end {
			n := copy(output, r.state.buffer[r.state.start:r.state.end])
			clear(r.state.buffer[r.state.start : r.state.start+n])
			r.state.start += n
			if r.state.start == r.state.end && r.state.pending != nil {
				r.state.terminalErr = r.state.pending
				return n, r.state.terminalErr
			}
			return n, nil
		}
		if r.state.terminalErr != nil {
			return 0, r.state.terminalErr
		}
		if noProgress >= 100 {
			return 0, webSocketFailure{1002}
		}
		copy(r.state.buffer[:], r.state.tail[:r.state.tailLength])
		n, err := r.state.input.Read(r.state.buffer[r.state.tailLength : webSocketChunk+r.state.tailLength])
		if err != nil && !errors.Is(err, io.EOF) {
			clear(r.state.buffer[:])
			r.state.terminalErr = err
			return 0, err
		}
		total := n + r.state.tailLength
		r.state.tailLength = 0
		position := 0
		for position < total {
			if !utf8.FullRune(r.state.buffer[position:total]) {
				if errors.Is(err, io.EOF) {
					r.state.terminalErr = webSocketFailure{1007}
					clear(r.state.buffer[:])
					return 0, r.state.terminalErr
				}
				r.state.tailLength = copy(r.state.tail[:], r.state.buffer[position:total])
				break
			}
			_, width := utf8.DecodeRune(r.state.buffer[position:total])
			if width == 1 && r.state.buffer[position] >= utf8.RuneSelf {
				r.state.terminalErr = webSocketFailure{1007}
				clear(r.state.buffer[:])
				return 0, r.state.terminalErr
			}
			position += width
		}
		r.state.start, r.state.end, r.state.pending = 0, position, err
		if position == 0 && err != nil {
			r.state.terminalErr = err
			return 0, err
		}
	}
}

func (r *webSocketTextReader) clear() {
	clear(r.state.buffer[:])
	clear(r.state.tail[:])
	r.state.input = nil
	r.state.pending, r.state.terminalErr = nil, nil
	r.state.start, r.state.end, r.state.tailLength = 0, 0, 0
}
