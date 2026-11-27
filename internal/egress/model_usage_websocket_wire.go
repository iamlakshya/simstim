package egress

import "bytes"

// The observer sees only canonical frames emitted by the existing frame
// engine, after successful guest writes. readWebSocketFrame remains the sole
// header grammar; this small carry assembles its at-most-14-byte input.
type modelUsageWebSocketWire struct {
	headersDone         bool
	headerMatch         int
	header              [14]byte
	headerN, headerNeed int
	frame               *webSocketFrame
	message             []byte
	messageOpen         bool
}

func (wire *modelUsageWebSocketWire) feed(payload []byte, message func([]byte) error) error {
	delimiter := [4]byte{'\r', '\n', '\r', '\n'}
	for len(payload) > 0 {
		if !wire.headersDone {
			value := payload[0]
			payload = payload[1:]
			if value == delimiter[wire.headerMatch] {
				wire.headerMatch++
			} else if value == '\r' {
				wire.headerMatch = 1
			} else {
				wire.headerMatch = 0
			}
			if wire.headerMatch == 4 {
				wire.headersDone = true
				wire.headerMatch = 0
			}
			continue
		}
		if wire.frame == nil {
			if wire.headerNeed == 0 {
				wire.headerNeed = 2
			}
			n := min(wire.headerNeed-wire.headerN, len(payload))
			copy(wire.header[wire.headerN:], payload[:n])
			wire.headerN += n
			payload = payload[n:]
			if wire.headerN < wire.headerNeed {
				continue
			}
			if wire.headerNeed == 2 {
				switch wire.header[1] & 127 {
				case 126:
					wire.headerNeed += 2
				case 127:
					wire.headerNeed += 8
				}
				if wire.header[1]&128 != 0 {
					wire.headerNeed += 4
				}
				if wire.headerN < wire.headerNeed {
					continue
				}
			}
			frame, err := readWebSocketFrame(bytes.NewReader(wire.header[:wire.headerNeed]), false)
			if err != nil {
				return err
			}
			wire.frame = frame
			wire.headerN, wire.headerNeed = 0, 0
			if frame.opcode < webSocketClose {
				if frame.opcode == 0 {
					if !wire.messageOpen {
						return webSocketFailure{1002}
					}
				} else {
					if wire.messageOpen {
						return webSocketFailure{1002}
					}
					wire.messageOpen = true
				}
				if frame.remaining > maxDecodedHTTPBody-uint64(len(wire.message)) {
					return webSocketFailure{1009}
				}
			}
		}
		frame := wire.frame
		if frame.remaining > 0 {
			n := len(payload)
			if uint64(n) > frame.remaining {
				n = int(frame.remaining)
			}
			if frame.opcode < webSocketClose {
				wire.message = append(wire.message, payload[:n]...)
			}
			payload = payload[n:]
			frame.remaining -= uint64(n)
			if frame.remaining > 0 {
				continue
			}
		}
		if frame.opcode < webSocketClose && frame.final {
			if err := message(wire.message); err != nil {
				return err
			}
			clear(wire.message)
			if cap(wire.message) > webSocketChunk {
				wire.message = nil
			} else {
				wire.message = wire.message[:0]
			}
			wire.messageOpen = false
		}
		wire.frame = nil
	}
	return nil
}
