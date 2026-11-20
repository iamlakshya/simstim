package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/provider"
)

// fakeProvider makes machines that live in memory, reached through a
// transport that serves a fake bridge.
type fakeProvider struct{ m *clienttest.Machine }

func (fakeProvider) Name() string { return "fake" }

func (fakeProvider) Create(_ context.Context, req provider.Request) (client.Host, error) {
	fmt.Fprintln(req.Out, "  made a fake machine")
	return client.Host{Name: req.Name, Machine: &client.Machine{Provider: "fake", ID: "fake-" + req.Name}}, nil
}

func (f fakeProvider) Open(client.Host) (client.Lifecycle, error) { return f.m, nil }

func withFake(t *testing.T, sessions []client.Session) (*clienttest.Machine, *clienttest.Transport) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TERM", "xterm-256color")
	tr := &clienttest.Transport{Sessions: sessions}
	m := clienttest.NewMachine(client.MachineRunning, tr)
	p := fakeProvider{m: m}
	old := providers
	providers = append(slices.Clone(providers), p)
	client.RegisterProvider(p.Name(), p.Open)
	t.Cleanup(func() { providers = old })
	return m, tr
}

func TestCreateThenDestroy(t *testing.T) {
	m, tr := withFake(t, nil)
	p, err := findProvider("fake")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := createHost(ctx, p, provider.Request{Name: "box", Out: os.Stdout}); err != nil {
		t.Fatal(err)
	}
	h, err := client.FindHost("box")
	if err != nil || h.Machine == nil || h.Machine.ID != "fake-box" {
		t.Fatalf("saved host = %+v, %v", h, err)
	}
	remotes := strings.Join(tr.Remotes(), "\n")
	if !strings.Contains(remotes, "uname -sm") || !strings.Contains(remotes, " bridge") {
		t.Fatalf("create did not bootstrap and connect over the provider's transport:\n%s", remotes)
	}
	if err := createHost(ctx, p, provider.Request{Name: "box", Out: os.Stdout}); err == nil {
		t.Fatal("a second host of the same name was created")
	}

	wrapper := filepath.Join(client.ConfigDir(), "ssh", "config-box")
	_ = os.MkdirAll(filepath.Dir(wrapper), 0o700)
	_ = os.WriteFile(wrapper, []byte("# generated\n"), 0o600)
	if err := destroyHost(ctx, quietPrompts(ctx), "box", false, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Calls(), "destroy") {
		t.Fatalf("the machine was not destroyed: %v", m.Calls())
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Fatal("the host is still in hosts.toml")
	}
	if _, err := os.Stat(wrapper); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the generated SSH config is still there: %v", err)
	}
}

func TestDestroyRefusesWhileASessionWorks(t *testing.T) {
	m, _ := withFake(t, []client.Session{
		{ID: "a", Name: "quiet", State: api.StateIdle, Alive: true},
		{ID: "b", Name: "busy", State: api.StateWorking, Alive: true},
	})
	if err := saveHost(context.Background(), client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}); err != nil {
		t.Fatal(err)
	}
	err := destroyHost(context.Background(), quietPrompts(context.Background()), "box", false, true)
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "busy") || strings.Contains(ue.Msg, "quiet") || !strings.Contains(ue.Hint, "--force") {
		t.Fatalf("err = %v, want a refusal that names the working session", err)
	}
	if slices.Contains(m.Calls(), "destroy") {
		t.Fatal("destroyed while a session was working")
	}
	if err := destroyHost(context.Background(), quietPrompts(context.Background()), "box", true, true); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.Calls(), "destroy") {
		t.Fatal("--force did not destroy it")
	}
}

func TestDestroyNeedsACreatedHost(t *testing.T) {
	withFake(t, nil)
	if err := saveHost(context.Background(), client.Host{Name: "mine", SSH: "me@example.com"}); err != nil {
		t.Fatal(err)
	}
	err := destroyHost(context.Background(), quietPrompts(context.Background()), "mine", false, true)
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Hint, "host rm mine") {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateChecksItsInput(t *testing.T) {
	withFake(t, nil)
	p, _ := findProvider("fake")
	for _, req := range []provider.Request{{Name: "bad name"}, {Name: "ok", IdleStop: "soon"}, {Name: "ok", IdleStop: "10s"}} {
		req.Out = os.Stdout
		var ue *client.UserError
		if err := createHost(context.Background(), p, req); !errors.As(err, &ue) {
			t.Errorf("%+v: err = %v, want a user error", req, err)
		}
	}
	_, err := findProvider("nope")
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Hint, "lima") {
		t.Fatalf("an unknown provider: %v", err)
	}
}

func TestMachineActions(t *testing.T) {
	m, _ := withFake(t, nil)
	h := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	ctx := context.Background()
	if err := machineAction(ctx, h, "stop"); err != nil {
		t.Fatal(err)
	}
	if err := machineAction(ctx, h, "start"); err != nil {
		t.Fatal(err)
	}
	if err := machineAction(ctx, h, "status"); err != nil {
		t.Fatal(err)
	}
	calls := strings.Join(m.Calls(), " ")
	if !strings.Contains(calls, "stop") || !strings.Contains(calls, "start") {
		t.Fatalf("calls = %s", calls)
	}
}
