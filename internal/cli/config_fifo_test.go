//go:build unix

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// mainWithin runs the command line as the binary does, and fails the test
// when it does not end within 10 s.
func mainWithin(t *testing.T, args ...string) (string, int) {
	t.Helper()
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = append([]string{brand.Name}, args...)
	type result struct {
		out  string
		code int
	}
	done := make(chan result, 1)
	go func() {
		out, code := captureStderr(t, Main)
		done <- result{out, code}
	}()
	select {
	case r := <-done:
		return r.out, r.code
	case <-time.After(10 * time.Second):
		t.Fatalf("%q did not end", args)
		return "", 0
	}
}

// Every command reads the config files through ReadConfigFile: a FIFO as
// config.toml makes `host ls` warn and go on with the defaults, and a FIFO
// as hosts.toml is an error that says what is wrong. Neither waits for a
// writer.
func TestConfigFilesThatAreFIFOs(t *testing.T) {
	configHome(t)
	settings := filepath.Join(client.ConfigDir(), "config.toml")
	if err := syscall.Mkfifo(settings, 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := mainWithin(t, "host", "ls")
	if code != 0 || !strings.Contains(out, "Warning: config.toml cannot be read, so its settings are not used.") {
		t.Errorf("host ls with a FIFO as config.toml = %d:\n%s", code, out)
	}
	// doctor reads the file for its own check, as config check does.
	if _, code := mainWithin(t, "doctor"); code != 0 {
		t.Errorf("doctor with a FIFO as config.toml = %d", code)
	}
	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(client.ConfigDir(), "hosts.toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code = mainWithin(t, "host", "ls")
	if code != 1 || !strings.Contains(out, "hosts.toml cannot be used: it is not a regular file.") || !strings.Contains(out, "Try: "+brand.Name+" config check") {
		t.Errorf("host ls with a FIFO as hosts.toml = %d:\n%s", code, out)
	}
}
