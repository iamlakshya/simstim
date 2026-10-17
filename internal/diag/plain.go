package diag

import (
	"errors"
	"fmt"
	"io"
	"regexp"
)

// debugName matches the names of the files that the debug mode writes in a
// debug folder: a process's records and their older part, its profiles,
// and its saved windows.
var debugName = regexp.MustCompile(`^(?:` +
	`(?:client|bridge)-[0-9]{8}-[0-9]{6}-[0-9]+(?:-[0-9]+)?` +
	`(?:\.jsonl(?:\.1)?|-(?:cpu|heap)\.pprof|-(?:auto-)?flight-[0-9]{8}-[0-9]{6}(?:-[0-9]+)?\.trace)` +
	`|(?:cpu|heap|goroutine)-[0-9]{8}-[0-9]{6}(?:-[0-9]+)?\.pprof` +
	`|(?:auto-)?flight-[0-9]{8}-[0-9]{6}(?:-[0-9]+)?\.trace)$`)

// IsDebugName reports whether name is one that the debug mode gives a file
// in a debug folder. A bundle takes no other file from a debug folder.
func IsDebugName(name string) bool { return debugName.MatchString(name) }

// ErrTooLarge is the error of a file larger than ReadPlain reads.
var ErrTooLarge = errors.New("it is larger than the bundle takes")

// ReadPlain reads a file that OpenPlain accepts, of at most max bytes.
func ReadPlain(path string, max int64) ([]byte, error) {
	f, err := OpenPlain(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("read %s: %w", path, ErrTooLarge)
	}
	return b, nil
}
