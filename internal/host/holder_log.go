package host

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/iamlakshya/simstim/internal/diag"
)

// newHolderLog returns the logger of a holder's log file: one line for each
// event, with a short message and its details as key=value text. It never
// gets a secret's value or the agent's output. Lines at INFO and above go
// to w. While the debug trace runs, lines at DEBUG and above also go to
// lines, when it is set.
func newHolderLog(w io.Writer, lines *diag.Lines) *slog.Logger {
	text := slog.NewTextHandler(w, nil)
	if lines == nil {
		return slog.New(text)
	}
	return slog.New(slog.NewMultiHandler(text, lines))
}

// holderLogMax is the size at which holder.log turns over to holder.log.1,
// as the alert delivery log does (decision 0025).
const holderLogMax = 1 << 20

// holderLog is a holder's log file. It turns over to the same name with
// ".1" added, replacing an older one, when a line would take it past max.
type holderLog struct {
	mu   sync.Mutex
	path string
	max  int64
	f    *os.File
	size int64
}

// openHolderLog opens path for appending, with mode 0600.
func openHolderLog(path string, max int64) (*holderLog, error) {
	l := &holderLog{path: path, max: max}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *holderLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open the holder log: %w", err)
	}
	var size int64
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}
	l.f, l.size = f, size
	return nil
}

// Write writes one line, which slog's handler writes in one call.
func (l *holderLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil && l.size > 0 && l.size+int64(len(p)) > l.max {
		l.f.Close()
		l.f = nil
		if err := os.Rename(l.path, l.path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, fmt.Errorf("turn over the holder log: %w", err)
		}
	}
	if l.f == nil {
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.f.Write(p)
	l.size += int64(n)
	if err != nil {
		return n, fmt.Errorf("write the holder log: %w", err)
	}
	return n, nil
}

// Close closes the file.
func (l *holderLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	if err != nil {
		return fmt.Errorf("close the holder log: %w", err)
	}
	return nil
}
