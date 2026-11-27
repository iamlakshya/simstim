package proto

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type pieceProgressWriter struct{ bytes.Buffer }

func (w *pieceProgressWriter) WriteProgress(p []byte, progress func()) (int, error) {
	total := len(p)
	for len(p) > 0 {
		part := p[:min(len(p), 7)]
		_, _ = w.Buffer.Write(part)
		if progress != nil {
			progress()
		}
		p = p[len(part):]
	}
	return total, nil
}

func TestProgressWriterKeepsCanonicalFrameBytes(t *testing.T) {
	payload := []byte{'a', 0, '\x1b', '[', '0', 'm', '\r', '\n', 'z'}
	var ordinary bytes.Buffer
	if err := NewWriter(&ordinary).Write(TOutput, payload); err != nil {
		t.Fatal(err)
	}
	progress := &pieceProgressWriter{}
	pieces := 0
	if err := NewWriter(progress).WriteProgress(TOutput, payload, func() { pieces++ }); err != nil {
		t.Fatal(err)
	}
	if pieces < 2 || !bytes.Equal(ordinary.Bytes(), progress.Bytes()) {
		t.Fatal("progress changed bytes or required whole-frame completion")
	}
}

type shortProgressWriter struct{}

func (shortProgressWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

func TestProgressFallbackDoesNotAcknowledgeShortFrame(t *testing.T) {
	called := false
	err := NewWriter(shortProgressWriter{}).WriteProgress(TOutput, []byte("data"), func() { called = true })
	if !errors.Is(err, io.ErrShortWrite) || called {
		t.Fatal("an incomplete generic write renewed output liveness", err)
	}
}
