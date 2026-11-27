package client

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := ReadConfigFile(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing file gives %v", err)
	}
	if err := os.WriteFile(path, []byte("[ui]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadConfigFile(path); err != nil || string(b) != "[ui]\n" {
		t.Errorf("a file gives %q, %v", b, err)
	}
	if _, err := ReadConfigFile(dir); !errors.Is(err, ErrNotAFile) {
		t.Errorf("a folder gives %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("#", MaxConfigSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfigFile(path); !errors.Is(err, ErrConfigTooLarge) {
		t.Errorf("a file over the limit gives %v", err)
	}
}

// endless is a regular file, by its own report, of 10 bytes, that goes on
// for 64 MiB: what a file that grows as it is read looks like.
type endless struct{ read int }

func (e *endless) Read(p []byte) (int, error) {
	if e.read >= 64<<20 {
		return 0, io.EOF
	}
	for i := range p {
		p[i] = '#'
	}
	e.read += len(p)
	return len(p), nil
}
func (e *endless) Close() error               { return nil }
func (e *endless) Stat() (os.FileInfo, error) { return smallFile{}, nil }

type smallFile struct{}

func (smallFile) Name() string       { return "config.toml" }
func (smallFile) Size() int64        { return 10 }
func (smallFile) Mode() fs.FileMode  { return 0o600 }
func (smallFile) ModTime() time.Time { return time.Time{} }
func (smallFile) IsDir() bool        { return false }
func (smallFile) Sys() any           { return nil }

// The limit bounds the read itself, not the size that the file reports.
func TestReadConfigFileBoundsTheRead(t *testing.T) {
	e := &endless{}
	old := openConfig
	openConfig = func(string) (configFile, error) { return e, nil }
	defer func() { openConfig = old }()
	done := make(chan error, 1)
	go func() {
		_, err := ReadConfigFile("config.toml")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrConfigTooLarge) || e.read > MaxConfigSize+1 {
			t.Errorf("an endless file gives %v, after %d bytes", err, e.read)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the read of an endless file did not stop")
	}
}
