package client_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
)

// fakeSSH puts an ssh on PATH that runs the remote command locally, or
// fails as ssh does when it cannot reach a host.
func fakeSSH(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake ssh is a POSIX shell script")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
for a; do last=$a; done
case "$*" in *unreachable*) echo "ssh: connect to host unreachable port 22: Connection refused" >&2; exit 255;; esac
exec sh -c "$last"
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestSSHTransportRoundTrip(t *testing.T) {
	fakeSSH(t)
	clienttest.RoundTrip(t, &client.SSH{Host: client.Host{Name: "box", SSH: "box"}})
}

func TestSSHTransportUnreachable(t *testing.T) {
	fakeSSH(t)
	s := &client.SSH{Host: client.Host{Name: "box", SSH: "unreachable"}}
	p, err := s.Start(context.Background(), "true")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(p.Stdout())
	if err := p.Wait(); !errors.Is(err, client.ErrUnreachable) {
		t.Fatalf("wait = %v, want ErrUnreachable", err)
	}
	if p.Stderr() == "" {
		t.Fatal("stderr was not kept")
	}
	if got := s.ReachHint(); got != "ssh unreachable   (check that this works first)" {
		t.Fatalf("hint = %q", got)
	}
}

func TestSSHTransportRun(t *testing.T) {
	fakeSSH(t)
	s := &client.SSH{Host: client.Host{Name: "box", SSH: "box"}}
	out, err := s.Run(context.Background(), "tr a-z A-Z", bytes.NewReader([]byte("hello")))
	if err != nil || string(out) != "HELLO" {
		t.Fatalf("run = %q, %v", out, err)
	}
}

func TestDialNotInstalledOverAnyTransport(t *testing.T) {
	fakeSSH(t)
	// The fake ssh runs the bridge command locally, where the helper is not
	// installed: the shell says "not found", which means "install it".
	h := client.Host{Name: "nowhere-" + filepath.Base(t.TempDir()), SSH: "box"}
	_, err := client.Dial(context.Background(), h, false)
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.HasSuffix(ue.Hint, " host add "+h.Name) {
		t.Fatalf("err = %v, want a user error to add the host", err)
	}
}

// An interactive ssh shows the user what the host writes to stderr, as it
// comes, but cleaned: ESC c, an OSC 52 write, a bidirectional override and
// a zero-width space never reach the terminal, when the command works and
// when it fails.
func TestInteractiveStderrIsClean(t *testing.T) {
	fakeSSH(t)
	const remote = `printf 'a\033c b\033]52;c;aGk=\007 c\342\200\256d\342\200\213e\nprompt: ' >&2`
	for _, tail := range []string{"", "; exit 3"} {
		f, err := os.CreateTemp(t.TempDir(), "stderr")
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stderr
		os.Stderr = f
		s := &client.SSH{Host: client.Host{Name: "box", SSH: "box"}, Interactive: true}
		_, runErr := s.Run(context.Background(), remote+tail, nil)
		os.Stderr = old
		_ = f.Close()
		got, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if want := "ac b]52;c;aGk= cde\nprompt: "; string(got) != want {
			t.Errorf("with %q the terminal got %q, want %q", tail, got, want)
		}
		if (tail == "") != (runErr == nil) {
			t.Errorf("with %q: %v", tail, runErr)
		}
		if runErr != nil && strings.ContainsAny(runErr.Error(), "\x1b\x07\u202e\u200b") {
			t.Errorf("the error holds the host's controls: %q", runErr.Error())
		}
	}
}
