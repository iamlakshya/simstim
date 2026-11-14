package client

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// MaxConfigSize is the largest config file that the client reads.
const MaxConfigSize = 1 << 20

// The errors of ReadConfigFile that are not the file system's.
var (
	ErrConfigTooLarge = errors.New("it is larger than 1 MiB")
	ErrNotAFile       = errors.New("it is not a regular file")
)

// configFile is an open config file.
type configFile interface {
	io.ReadCloser
	Stat() (os.FileInfo, error)
}

// openConfig opens a config file for ReadConfigFile. O_NONBLOCK lets a
// FIFO open at once, so that Stat can refuse it: without it, the open
// waits for a writer. Tests replace openConfig.
var openConfig = func(path string) (configFile, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err //nolint:wrapcheck // ReadConfigFile wraps it
	}
	return f, nil
}

// ReadConfigFile reads a config file of at most MaxConfigSize bytes. It
// opens the file once, and asks that open file what it is, so that a file
// that changes between two looks cannot pass. It refuses anything but a
// regular file, such as a FIFO or a device, and it reads no more than one
// byte past the limit, whatever size the file reports.
func ReadConfigFile(path string) ([]byte, error) {
	f, err := openConfig(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()
	fi, err := f.Stat()
	switch {
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	case !fi.Mode().IsRegular():
		return nil, ErrNotAFile
	case fi.Size() > MaxConfigSize:
		return nil, ErrConfigTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxConfigSize+1))
	switch {
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	case len(b) > MaxConfigSize:
		return nil, ErrConfigTooLarge
	}
	return b, nil
}
