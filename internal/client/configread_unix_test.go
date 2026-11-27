//go:build unix

package client

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO, and a link to a device, are no config files: they are refused at
// once, without a wait for a writer and without a read.
func TestReadConfigFileRefusesOtherFiles(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo.toml")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	zero := filepath.Join(dir, "zero.toml")
	if err := os.Symlink("/dev/zero", zero); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, zero} {
		done := make(chan error, 1)
		go func() {
			_, err := ReadConfigFile(path)
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, ErrNotAFile) {
				t.Errorf("%s gives %v", filepath.Base(path), err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not return", filepath.Base(path))
		}
	}
}
