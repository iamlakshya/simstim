package docker_test

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

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/provider"
	"github.com/iamlakshya/simstim/internal/provider/docker"
)

// fakeDocker is a docker on PATH that keeps each container's state in a
// file and runs `exec` commands locally. It logs every call.
const fakeDocker = `#!/bin/sh
echo "$*" >> "$FAKE_DIR/log"
cmd=$1; shift
state() { cat "$FAKE_DIR/state-$1" 2>/dev/null; }
case $cmd in
inspect)
  name=$3
  if [ -f "$FAKE_DIR/state-$name" ]; then state "$name"; else echo "${FAKE_MISSING:-Error: No such object}: $name" >&2; exit 1; fi ;;
run)
  while [ "$1" != "--name" ]; do shift; done
  echo running > "$FAKE_DIR/state-$2"; echo 0123456789ab ;;
exec)
  while [ "$1" != "sh" ]; do last=$1; shift; done
  if [ "$(state "$last")" != running ]; then echo "Error response from daemon: container $last is not running" >&2; exit 1; fi
  shift; shift
  case $1 in *useradd*) exit 0 ;; esac # no users are made on this machine
  exec sh -c "$1" ;;
start|unpause) echo running > "$FAKE_DIR/state-$1" ;;
stop) echo exited > "$FAKE_DIR/state-$3" ;;
rm) rm -f "$FAKE_DIR/state-$2" ;;
*) echo "fake docker: unknown command $cmd" >&2; exit 2 ;;
esac
`

func setup(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake docker is a POSIX shell script")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(fakeDocker), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DIR", dir)
	return dir
}

func calls(dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "log"))
	return string(b)
}

func TestCreateAndLifecycle(t *testing.T) {
	dir := setup(t)
	ctx := context.Background()
	var out bytes.Buffer
	h, err := docker.Provider{}.Create(ctx, provider.Request{Name: "box", Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	c := provider.MachineName("box")
	if h.Name != "box" || h.SSH != "" || h.Machine == nil || h.Machine.Provider != "docker" || h.Machine.ID != c || h.Machine.User != docker.DefaultUser || h.Machine.Image != docker.DefaultImage {
		t.Fatalf("host = %+v, machine = %+v", h, h.Machine)
	}
	log := calls(dir)
	want := "run -d --init --name " + c + " --label " + brand.Name + ".host=box " + docker.DefaultImage + " sleep infinity"
	if !strings.Contains(log, want) {
		t.Fatalf("docker calls:\n%s\nwant %q", log, want)
	}
	if !strings.Contains(log, "exec -u 0 "+c+" sh -c id -u user") {
		t.Fatalf("no user was added:\n%s", log)
	}
	if !strings.Contains(out.String(), "docker run") {
		t.Fatalf("create did not say what it ran: %q", out.String())
	}

	// A second create with the same name is refused.
	if _, err := (docker.Provider{}).Create(ctx, provider.Request{Name: "box"}); err == nil {
		t.Fatal("a second container of the same name was made")
	}

	lc, err := docker.Provider{}.Open(h)
	if err != nil {
		t.Fatal(err)
	}
	st := func() string {
		s, err := lc.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := st(); s != client.MachineRunning {
		t.Fatalf("status = %s", s)
	}
	if err := lc.Stop(ctx); err != nil || st() != client.MachineStopped {
		t.Fatalf("stop: %v, status %s", err, st())
	}
	// The transport says the container cannot be reached while it is stopped.
	tr, _ := lc.Transport(ctx, false)
	if _, err := tr.Run(ctx, "true", nil); err == nil || !strings.Contains(err.Error(), "cannot reach") {
		t.Fatalf("run on a stopped container: %v", err)
	}
	p, err := tr.Start(ctx, "true")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(p.Stdout())
	if err := p.Wait(); !errors.Is(err, client.ErrUnreachable) {
		t.Fatalf("wait = %v, want ErrUnreachable", err)
	}
	if err := lc.Start(ctx); err != nil || st() != client.MachineRunning {
		t.Fatalf("start: %v, status %s", err, st())
	}
	if err := lc.Destroy(ctx); err != nil || st() != client.MachineMissing {
		t.Fatalf("destroy: %v, status %s", err, st())
	}
	// Destroying what is gone is fine.
	if err := lc.Destroy(ctx); err != nil {
		t.Fatal(err)
	}
}

// Docker words a missing container in more than one way, and newer Docker
// writes it in lower case. Each one means "no container", not an error.
func TestMissingContainerWording(t *testing.T) {
	for _, msg := range []string{
		"Error: No such object",
		"error: no such object",
		"Error response from daemon: No such container",
	} {
		t.Run(msg, func(t *testing.T) {
			setup(t)
			t.Setenv("FAKE_MISSING", msg)
			ctx := context.Background()
			h, err := docker.Provider{}.Create(ctx, provider.Request{Name: "box"})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			lc, err := docker.Provider{}.Open(h)
			if err != nil {
				t.Fatal(err)
			}
			if err := lc.Destroy(ctx); err != nil {
				t.Fatal(err)
			}
			if s, err := lc.Status(ctx); err != nil || s != client.MachineMissing {
				t.Fatalf("status after destroy = %s, %v", s, err)
			}
		})
	}
}

func TestTransportRoundTrip(t *testing.T) {
	setup(t)
	if _, err := (docker.Provider{}).Create(context.Background(), provider.Request{Name: "rt"}); err != nil {
		t.Fatal(err)
	}
	clienttest.RoundTrip(t, &docker.Transport{Container: provider.MachineName("rt"), User: "user"})
}

func TestTransportRun(t *testing.T) {
	setup(t)
	if _, err := (docker.Provider{}).Create(context.Background(), provider.Request{Name: "run"}); err != nil {
		t.Fatal(err)
	}
	tr := &docker.Transport{Container: provider.MachineName("run"), User: "user"}
	out, err := tr.Run(context.Background(), "tr a-z A-Z; exit 0", strings.NewReader("abc"))
	if err != nil || string(out) != "ABC" {
		t.Fatalf("run = %q, %v", out, err)
	}
	if _, err := tr.Run(context.Background(), "echo oops >&2; exit 3", nil); err == nil || !strings.Contains(err.Error(), "oops") {
		t.Fatalf("a failing command: %v", err)
	}
	// What the command writes to stderr is the host's text: the error keeps
	// it cleaned, ESC c and OSC 52 and hidden characters gone.
	_, err = tr.Run(context.Background(), `printf 'a\033c b\033]52;c;aGk=\007 c\342\200\256d\342\200\213e\n' >&2; exit 3`, nil)
	if err == nil || !strings.Contains(err.Error(), "ac b]52;c;aGk= cde") || strings.ContainsAny(err.Error(), "\x1b\x07\u202e\u200b") {
		t.Fatalf("a failing command with a hostile stderr: %q", err)
	}
	if h := tr.ReachHint(); !strings.Contains(h, "docker exec -it -u user") {
		t.Fatal(h)
	}
}

func TestMissingDocker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH handling differs")
	}
	t.Setenv("PATH", t.TempDir())
	_, err := docker.Provider{}.Create(context.Background(), provider.Request{Name: "x"})
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Hint, "Install Docker") {
		t.Fatalf("err = %v, want a user error that says to install Docker", err)
	}
}
