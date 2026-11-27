package docker_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/provider"
	"github.com/iamlakshya/simstim/internal/provider/docker"
)

func TestCreateAndLifecycle(t *testing.T) {
	dir := setup(t)
	ctx := context.Background()
	var out bytes.Buffer
	h, err := fixtureCreate(t, ctx, provider.Request{Name: "box", Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	c := provider.MachineName("box")
	if h.Machine == nil || h.Machine.Source == nil || h.Machine.Source.ResourceID != fixtureResource || h.Machine.Source.EngineID != fixtureEngine || h.Name != "box" || h.SSH != "" || h.Machine.Provider != "docker" || h.Machine.ID != c || h.Machine.User != docker.DefaultUser || h.Machine.Image != docker.DefaultImage {
		t.Fatalf("host = %+v, machine = %+v", h, h.Machine)
	}
	log := calls(dir)
	want := "run -d --init --name " + c + " --label " + brand.Name + ".host=box " + docker.DefaultImage + " sleep infinity"
	if !strings.Contains(log, want) {
		t.Fatalf("docker calls:\n%s\nwant %q", log, want)
	}
	if !strings.Contains(log, "exec -u 0 "+fixtureResource+" sh -c id -u user") {
		t.Fatalf("no user was added:\n%s", log)
	}
	if !strings.Contains(out.String(), "docker run") {
		t.Fatalf("create did not say what it ran: %q", out.String())
	}

	// A second create with the same name is refused.
	if _, err := fixtureCreate(t, ctx, provider.Request{Name: "box"}); err == nil {
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
			h, err := fixtureCreate(t, ctx, provider.Request{Name: "box"})
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
	if _, err := fixtureCreate(t, context.Background(), provider.Request{Name: "rt"}); err != nil {
		t.Fatal(err)
	}
	clienttest.RoundTrip(t, &docker.Transport{Container: provider.MachineName("rt"), User: "user"})
}

func TestTransportRun(t *testing.T) {
	setup(t)
	if _, err := fixtureCreate(t, context.Background(), provider.Request{Name: "run"}); err != nil {
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
	for _, selected := range []string{"", "selected-context"} {
		t.Run(selected, func(t *testing.T) {
			isolateDockerHome(t)
			t.Setenv("DOCKER_CONTEXT", selected)
			t.Setenv("PATH", t.TempDir())
			_, err := fixtureCreate(t, t.Context(), provider.Request{Name: "x"})
			var unavailable *client.UserError
			if !errors.As(err, &unavailable) || unavailable.Code != "tool_missing" || !strings.Contains(unavailable.Hint, "Install Docker") {
				t.Fatalf("err = %v, want a user error that says to install Docker", err)
			}
		})
	}
}
