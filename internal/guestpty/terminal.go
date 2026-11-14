package guestpty

import (
	"fmt"
	"io"
	"sync"

	"github.com/iamlakshya/simstim/internal/proto"
)

// Terminal is the holder's data-plane adapter. It observes only framed
// results; no process handle, credential switch or operating-system wait
// belongs here. Pipe backpressure keeps at most one bounded output frame.
type Terminal struct {
	channel *Channel
	reader  *io.PipeReader
	writer  *io.PipeWriter
	done    chan struct{}
	once    sync.Once
	result  proto.Ended
	err     error
}

// NewTerminal adopts the dedicated holder-role channel supplied by init.
func NewTerminal(channel *Channel) (*Terminal, error) {
	if channel == nil || channel.role != Holder {
		return nil, fmt.Errorf("the holder terminal requires its scoped inherited channel")
	}
	reader, writer := io.Pipe()
	t := &Terminal{channel: channel, reader: reader, writer: writer, done: make(chan struct{})}
	go t.output()
	return t, nil
}

func (t *Terminal) output() {
	defer close(t.done)
	defer t.channel.Close()
	for {
		frame, err := t.channel.Read()
		if err != nil {
			t.err = err
			t.writer.CloseWithError(err)
			return
		}
		switch frame.Type {
		case proto.TOutput:
			if _, err := t.writer.Write(frame.Payload); err != nil {
				t.err = err
				return
			}
		case proto.TEnded:
			if err := DecodeControl(frame.Payload, &t.result); err != nil {
				t.err = err
				t.writer.CloseWithError(err)
				return
			}
			t.writer.Close()
			return
		default:
			t.err = fmt.Errorf("unexpected holder terminal frame")
			t.writer.CloseWithError(t.err)
			return
		}
	}
}

// Read gives the unchanged PTY bytes to the existing holder ring/VT pump.
func (t *Terminal) Read(data []byte) (int, error) { return t.reader.Read(data) }

// Write sends unchanged input to this init-owned run.
func (t *Terminal) Write(data []byte) (int, error) {
	if err := t.channel.Input(data); err != nil {
		return 0, err
	}
	return len(data), nil
}

// Resize asks init to resize only its still-owned PTY.
func (t *Terminal) Resize(size proto.Resize) error { return t.channel.Resize(size) }

// Signal asks init to signal only this still-owned run.
func (t *Terminal) Signal(number uint32) error { return t.channel.Signal(number) }

// Close aborts this data plane. Init treats EOF as cancellation of the SAME
// owned run and performs its existing last-signal-before-reap cleanup.
func (t *Terminal) Close() error {
	t.once.Do(func() { t.reader.Close(); t.writer.Close(); t.channel.Close() })
	return nil
}

// Wait observes one terminal result after the last output reached the
// holder reader. It performs no PID lookup or operating-system wait.
func (t *Terminal) Wait() (proto.Ended, error) { <-t.done; return t.result, t.err }
