package proto

import (
	"bufio"
	"errors"
	"fmt"
)

// ReadJSONLine reads one metadata line without discarding bytes buffered after
// its newline. Callers keep this same reader for subsequent binary frames.
// The line, including its final newline, cannot exceed MaxFrame.
func ReadJSONLine(reader *bufio.Reader) ([]byte, error) {
	if reader == nil {
		return nil, fmt.Errorf("metadata line has no reader")
	}
	var data []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > MaxFrame-len(data) {
			return nil, fmt.Errorf("metadata line exceeds the protocol bound")
		}
		data = append(data, fragment...)
		if err == nil {
			return data, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, fmt.Errorf("read metadata line: %w", err)
		}
	}
}
