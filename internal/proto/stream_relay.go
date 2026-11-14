package proto

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// RelayStreams forwards only whole stream frames, unchanged. One reusable
// buffer uses the link's existing64KiB frame bound, never the4MiB request cap.
func RelayStreams(r *bufio.Reader, w *Writer) error {
	data := make([]byte, 64<<10)
	for {
		var head [4]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return err
		}
		n := binary.LittleEndian.Uint32(head[:])
		if n < 5 || n > uint32(len(data)) {
			return fmt.Errorf("invalid streaming frame size. Try: update the client and helper")
		}
		frame := data[:n]
		if _, err := io.ReadFull(r, frame); err != nil {
			return err
		}
		switch Type(frame[0]) {
		case TStreamOpen, TStreamData, TStreamClose, TStreamWindow:
		default:
			return fmt.Errorf("unexpected frame during a dedicated stream. Try: open a fresh streaming connection")
		}
		if err := w.Write(Type(frame[0]), frame[1:]); err != nil {
			return err
		}
	}
}
