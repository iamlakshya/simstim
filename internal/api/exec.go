package api

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// Exec stream record tags (isolation spec 10.2).
const (
	ExecRecordExit   byte = 0 // followed by u32 little-endian exit code (128+N for signal)
	ExecRecordStdout byte = 1 // followed by u32 little-endian length and bytes
	ExecRecordStderr byte = 2 // followed by u32 little-endian length and bytes

	// MaxExecRecordSize bounds one stdout/stderr record on an exec stream so
	// a hostile peer cannot trigger a huge allocation.
	MaxExecRecordSize = 64 << 10
	execChunkSize     = 32 << 10

	// DefaultExecTimeoutMS is the default 10-minute command timeout (spec 6.11).
	DefaultExecTimeoutMS int64 = 10 * 60 * 1000

	// KeepRefusedCode is the error code for a refused [sandbox] keep path.
	KeepRefusedCode = "keep_refused"
)

// ExecArgs requests a one-shot non-interactive command inside a sandbox
// (isolation spec 6.11, 10.3, 10.5).
type ExecArgs struct {
	ID        string   `json:"id"`
	ExecID    string   `json:"exec_id,omitempty"`
	Argv      []string `json:"argv"`
	Cwd       string   `json:"cwd,omitempty"`
	TimeoutMS int64    `json:"timeout_ms,omitempty"`
	// NoTimeout disables the default command timeout when true (CLI --timeout 0).
	NoTimeout bool `json:"no_timeout,omitempty"`
}

// ExecResponse is the bridge's TResponse before switching the connection to
// an internal/link streaming session carrying KindExec (spec 10.5).
type ExecResponse struct {
	ExecID string `json:"exec_id"`
}

// WriteExecChunk writes data as one or more stdout/stderr records of kind
// (ExecRecordStdout or ExecRecordStderr) to w.
func WriteExecChunk(w io.Writer, kind byte, data []byte) error {
	if kind != ExecRecordStdout && kind != ExecRecordStderr {
		return fmt.Errorf("invalid exec record kind %d", kind)
	}
	for len(data) > 0 {
		n := min(len(data), execChunkSize)
		var hdr [5]byte
		hdr[0] = kind
		binary.LittleEndian.PutUint32(hdr[1:], uint32(n))
		if _, err := w.Write(hdr[:]); err != nil {
			return fmt.Errorf("write exec chunk header: %w", err)
		}
		if _, err := w.Write(data[:n]); err != nil {
			return fmt.Errorf("write exec chunk payload: %w", err)
		}
		data = data[n:]
	}
	return nil
}

// WriteExecExit writes the terminating exit-code record (tag 0 + u32 LE) to w.
func WriteExecExit(w io.Writer, code int) error {
	var rec [5]byte
	rec[0] = ExecRecordExit
	if code < 0 {
		code = 125
	}
	binary.LittleEndian.PutUint32(rec[1:], uint32(code))
	if _, err := w.Write(rec[:]); err != nil {
		return fmt.Errorf("write exec exit record: %w", err)
	}
	return nil
}

// ReadExecStream reads spec 10.2 exec records from r, demultiplexing stdout
// and stderr until the terminating exit-code record arrives. It returns the
// command's exit code (0..255 or 128+signal), or an error if the stream ends
// before the exit record or carries a malformed record.
func ReadExecStream(r io.Reader, stdout, stderr io.Writer) (int, error) {
	var hdr [5]byte
	var buf []byte
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return 125, fmt.Errorf("exec stream ended before the command returned an exit code: %w", err)
			}
			return 125, fmt.Errorf("read exec record header: %w", err)
		}
		tag := hdr[0]
		n := binary.LittleEndian.Uint32(hdr[1:])
		switch tag {
		case ExecRecordExit:
			return int(n), nil
		case ExecRecordStdout:
			var err error
			buf, err = readExecChunk(r, n, buf, stdout)
			if err != nil {
				return 125, err
			}
		case ExecRecordStderr:
			var err error
			buf, err = readExecChunk(r, n, buf, stderr)
			if err != nil {
				return 125, err
			}
		default:
			return 125, fmt.Errorf("unknown exec stream record tag %d", tag)
		}
	}
}

func readExecChunk(r io.Reader, n uint32, buf []byte, dst io.Writer) ([]byte, error) {
	if n > MaxExecRecordSize {
		return buf, fmt.Errorf("exec stream record of %d bytes exceeds limit %d", n, MaxExecRecordSize)
	}
	if n == 0 {
		return buf, nil
	}
	if cap(buf) < int(n) {
		buf = make([]byte, n)
	}
	chunk := buf[:n]
	if _, err := io.ReadFull(r, chunk); err != nil {
		return buf, fmt.Errorf("read exec stream payload: %w", err)
	}
	if dst != nil {
		if _, err := dst.Write(chunk); err != nil {
			return buf, fmt.Errorf("write exec output: %w", err)
		}
	}
	return buf, nil
}

// refusedKeepPaths lists shell startup and hook files that [sandbox] keep
// refuses so a dropper cannot persist across sandbox starts (spec 6.11, 7.1).
var refusedKeepPaths = map[string]bool{
	".bashrc":                     true,
	".bash_profile":               true,
	".bash_login":                 true,
	".bash_logout":                true,
	".profile":                    true,
	".zshrc":                      true,
	".zprofile":                   true,
	".zshenv":                     true,
	".zlogin":                     true,
	".zlogout":                    true,
	".kshrc":                      true,
	".cshrc":                      true,
	".tcshrc":                     true,
	".login":                      true,
	".config/fish/config.fish":    true,
	".claude/settings.json":       true,
	".claude/settings.local.json": true,
}

// CheckKeepPath validates one [sandbox] keep entry and returns its normalized
// relative path under /home/agent, or an error describing why it is refused.
func CheckKeepPath(raw string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return "", fmt.Errorf("sandbox.keep entry %q must be a relative file path under the home folder", raw)
	}
	if strings.ContainsAny(raw, "\x00\r\n\\") {
		return "", fmt.Errorf("sandbox.keep entry %q contains a disallowed character", raw)
	}
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "~") || strings.HasSuffix(raw, "/") {
		return "", fmt.Errorf("sandbox.keep refuses %q: must be a relative file path under the home folder", raw)
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("sandbox.keep refuses %q: path escapes the home folder", raw)
	}
	if refusedKeepPaths[clean] {
		return "", fmt.Errorf("sandbox.keep refuses %q because shells or agents run it on start", clean)
	}
	// Also refuse fish conf.d scripts (e.g. .config/fish/conf.d/foo.fish).
	if strings.HasPrefix(clean, ".config/fish/conf.d/") || clean == ".config/fish/conf.d" {
		return "", fmt.Errorf("sandbox.keep refuses %q because fish runs it on start", clean)
	}
	return clean, nil
}
