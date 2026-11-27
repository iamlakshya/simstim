package link

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Exec record channels. Output uses End/Stdout/Stderr; input uses Stdin/EOF/Signal.
const (
	ExecEnd byte = iota
	ExecStdout
	ExecStderr
	ExecStdin
	ExecStdinEOF
	ExecSignal
	ExecAlive
)

// ExecRecordMax bounds one record using the existing stream credit window.
const ExecRecordMax = DefaultWindow

// ExecRecords serialises complete records without changing payload bytes.
// Output end and stdin EOF are independent half-close states.
type ExecRecords struct {
	mu                sync.Mutex
	writer            io.Writer
	ended, inputEnded bool
	failed            error
}

// NewExecRecords writes records to one already owned exec stream.
func NewExecRecords(writer io.Writer) *ExecRecords { return &ExecRecords{writer: writer} }

// Output writes stdout or stderr bytes, chunked into bounded exact records.
func (e *ExecRecords) Output(channel byte, data []byte) (int, error) {
	if channel != ExecStdout && channel != ExecStderr {
		return 0, execProtocol("invalid output channel")
	}
	return e.data(channel, data)
}

// Input writes stdin bytes without treating terminal escapes or keys specially.
func (e *ExecRecords) Input(data []byte) (int, error) { return e.data(ExecStdin, data) }

func (e *ExecRecords) data(channel byte, data []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failed != nil {
		return 0, e.failed
	}
	if e.ended || channel == ExecStdin && e.inputEnded {
		return 0, execProtocol("data after end")
	}
	written := 0
	for len(data) > 0 {
		count := min(len(data), ExecRecordMax)
		var header [5]byte
		header[0] = channel
		binary.LittleEndian.PutUint32(header[1:], uint32(count))
		if err := e.write(header[:]); err != nil {
			return written, err
		}
		if err := e.write(data[:count]); err != nil {
			return written, err
		}
		written += count
		data = data[count:]
	}
	return written, nil
}

// End writes exactly one terminal exit code after the last output record.
func (e *ExecRecords) End(code int) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if code < 0 || code > 255 {
		return execProtocol("exit code outside 0..255")
	}
	if e.failed != nil {
		return e.failed
	}
	if e.ended {
		return execProtocol("duplicate terminal record")
	}
	e.ended = true
	return e.control(ExecEnd, uint32(code))
}

// CloseInput marks stdin EOF without ending command output.
func (e *ExecRecords) CloseInput() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failed != nil {
		return e.failed
	}
	if e.ended {
		return execProtocol("control after terminal result")
	}
	if e.inputEnded {
		return nil
	}
	e.inputEnded = true
	return e.control(ExecStdinEOF, 0)
}

// Signal forwards only a deliberate Unix signal number to the owned command.
func (e *ExecRecords) Signal(signal uint32) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !validExecSignal(signal) {
		return execProtocol("invalid signal")
	}
	if e.ended {
		return execProtocol("signal after terminal result")
	}
	if e.failed != nil {
		return e.failed
	}
	return e.control(ExecSignal, signal)
}

// KeepAlive keeps the dedicated transport live after stdin EOF.
func (e *ExecRecords) KeepAlive() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ended {
		return execProtocol("heartbeat after terminal result")
	}
	if e.failed != nil {
		return e.failed
	}
	return e.control(ExecAlive, 0)
}

func (e *ExecRecords) control(channel byte, value uint32) error {
	var record [5]byte
	record[0] = channel
	binary.LittleEndian.PutUint32(record[1:], value)
	return e.write(record[:])
}
func (e *ExecRecords) write(data []byte) error {
	for len(data) > 0 {
		count, err := e.writer.Write(data)
		if count < 0 || count > len(data) {
			e.failed = io.ErrShortWrite
			return e.failed
		}
		if count > 0 {
			data = data[count:]
		}
		if err != nil {
			e.failed = err
			return err
		}
		if count == 0 {
			e.failed = io.ErrShortWrite
			return e.failed
		}
	}
	return nil
}

// ReadExecOutput copies separate stdout/stderr channels verbatim. A terminal
// result is accepted only with immediate stream EOF, never with trailing data.
func ReadExecOutput(reader io.Reader, stdout, stderr io.Writer) (int, error) {
	buffer := make([]byte, ExecRecordMax)
	for {
		var header [5]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return 125, fmt.Errorf("read exec record: %w", err)
		}
		value := binary.LittleEndian.Uint32(header[1:])
		switch header[0] {
		case ExecEnd:
			if value > 255 {
				return 125, execProtocol("invalid terminal exit code")
			}
			var extra [1]byte
			count, err := io.ReadFull(reader, extra[:])
			if count != 0 || !errors.Is(err, io.EOF) {
				return 125, execProtocol("data or missing EOF after terminal result")
			}
			return int(value), nil
		case ExecStdout, ExecStderr:
			if value > ExecRecordMax {
				return 125, execProtocol("oversized output record")
			}
			if _, err := io.ReadFull(reader, buffer[:value]); err != nil {
				return 125, fmt.Errorf("read exec output: %w", err)
			}
			target := stdout
			if header[0] == ExecStderr {
				target = stderr
			}
			if target == nil {
				target = io.Discard
			}
			if err := writeExecBytes(target, buffer[:value]); err != nil {
				return 125, fmt.Errorf("write exec output: %w", err)
			}
		default:
			return 125, execProtocol("unknown output record")
		}
	}
}

// ReadExecInput applies bounded stdin and signal records. A closed input stream
// calls cancel, so a disconnected client cannot leave an owned command running.
func ReadExecInput(reader io.Reader, stdin io.WriteCloser, signal func(uint32) error, cancel func()) error {
	defer cancel()
	defer stdin.Close()
	buffer := make([]byte, ExecRecordMax)
	closed := false
	for {
		var header [5]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return err
		}
		value := binary.LittleEndian.Uint32(header[1:])
		switch header[0] {
		case ExecStdin:
			if closed || value > ExecRecordMax {
				return execProtocol("invalid stdin record")
			}
			if _, err := io.ReadFull(reader, buffer[:value]); err != nil {
				return err
			}
			if err := writeExecBytes(stdin, buffer[:value]); err != nil {
				return err
			}
		case ExecStdinEOF:
			if value != 0 {
				return execProtocol("invalid stdin EOF")
			}
			if !closed {
				closed = true
				if err := stdin.Close(); err != nil {
					return err
				}
			}
		case ExecAlive:
			if value != 0 {
				return execProtocol("invalid heartbeat record")
			}
		case ExecSignal:
			if !validExecSignal(value) {
				return execProtocol("invalid signal record")
			}
			if err := signal(value); err != nil {
				return err
			}
		default:
			return execProtocol("unknown input record")
		}
	}
}

func execProtocol(message string) error {
	return fmt.Errorf("exec stream: %s. Try: update the client and helper, then run the command again", message)
}

func writeExecBytes(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n < 0 || n > len(p) {
			return io.ErrShortWrite
		}
		p = p[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func validExecSignal(number uint32) bool {
	switch number {
	case 1, 2, 3, 9, 15:
		return true
	}
	return false
}
