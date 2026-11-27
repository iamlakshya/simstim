package proto

import (
	"encoding/json"
	"fmt"
	"io"
)

// WriteProgress writes the SAME complete frame under the SAME writer turn.
// A progress-aware owned sink reports only actual completed output pieces.
// The callback must not block; it carries liveness, never frame completion.
func (w *Writer) WriteProgress(t Type, payload []byte, progress func()) error {
	buf, err := frame(t, payload)
	if err != nil {
		return err
	}
	w.turn <- struct{}{}
	defer func() { <-w.turn }()
	var n int
	if output, ok := w.w.(interface {
		WriteProgress([]byte, func()) (int, error)
	}); ok {
		n, err = output.WriteProgress(buf, progress)
	} else {
		n, err = w.w.Write(buf)
		if err == nil && n == len(buf) && progress != nil {
			progress()
		}
	}
	if err == nil && n != len(buf) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return fmt.Errorf("proto: write a frame: %w", err)
	}
	return nil
}

// WriteJSONProgress encodes as WriteJSON and uses the same progress-aware turn.
func (w *Writer) WriteJSONProgress(t Type, value any, progress func()) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("proto: encode a frame: %w", err)
	}
	return w.WriteProgress(t, payload, progress)
}
