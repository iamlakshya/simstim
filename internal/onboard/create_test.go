package onboard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/provider"
)

// fakeProvider makes machines in memory. With block, Create waits for its
// context to end, as a slow VM would when the user presses Ctrl+C.
type fakeProvider struct {
	name  string
	m     *clienttest.Machine
	block bool
}

func (p fakeProvider) Name() string { return p.name }

func (p fakeProvider) Create(ctx context.Context, req provider.Request) (client.Host, error) {
	fmt.Fprintln(req.Out, "  made a fake machine")
	if p.block {
		p.m.SetState(client.MachineStarting)
		<-ctx.Done()
		return client.Host{}, ctx.Err()
	}
	p.m.SetState(client.MachineRunning)
	return client.Host{Name: req.Name, Machine: &client.Machine{Provider: p.name, ID: provider.MachineName(req.Name)}}, nil
}

func (p fakeProvider) Open(client.Host) (client.Lifecycle, error) { return p.m, nil }

func newFake(t *testing.T, name string) fakeProvider {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := clienttest.NewMachine(client.MachineMissing, &clienttest.Transport{})
	client.RegisterProvider(name, func(client.Host) (client.Lifecycle, error) { return m, nil })
	return fakeProvider{name: name, m: m}
}

func TestCreateSavesTheHost(t *testing.T) {
	p := newFake(t, "fake-save")
	h, err := Create(context.Background(), p, provider.Request{Name: "box", Out: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := client.FindHost("box")
	if err != nil || saved.Machine == nil || saved.Machine.ID != h.Machine.ID {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	for _, req := range []provider.Request{{Name: "box"}, {Name: "bad name"}, {Name: "ok", IdleStop: "10s"}} {
		var ue *client.UserError
		if _, err := Create(context.Background(), p, req); !errors.As(err, &ue) {
			t.Errorf("%+v: err = %v, want a user error", req, err)
		}
	}
}

func TestStopDeletesAHalfMadeMachine(t *testing.T) {
	p := newFake(t, Docker)
	p.block = true
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	_, err := Create(ctx, p, provider.Request{Name: "box", Out: io.Discard})
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "half-made docker machine") {
		t.Fatalf("err = %v", err)
	}
	if !slices.Contains(p.m.Calls(), "destroy") {
		t.Fatalf("the half-made machine was not deleted: %v", p.m.Calls())
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Fatal("a stopped create saved its host")
	}
}

func TestStopKeepsAMachineThatWasThere(t *testing.T) {
	p := newFake(t, Lima)
	p.block = true
	p.m.SetState(client.MachineStopped) // made by someone else, before
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	if _, err := Create(ctx, p, provider.Request{Name: "box", Out: io.Discard}); err == nil {
		t.Fatal("no error")
	}
	if slices.Contains(p.m.Calls(), "destroy") {
		t.Fatal("a machine that was there before was deleted")
	}
}

func TestStopOnAProviderWithoutNames(t *testing.T) {
	p := newFake(t, E2B)
	p.block = true
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	_, err := Create(ctx, p, provider.Request{Name: "box", Out: io.Discard})
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "lease ends") || !strings.Contains(ue.Hint, "console") {
		t.Fatalf("err = %v", err)
	}
	if slices.Contains(p.m.Calls(), "status") || slices.Contains(p.m.Calls(), "destroy") {
		t.Fatalf("calls = %v", p.m.Calls())
	}
}

func TestSetIdle(t *testing.T) {
	p := newFake(t, "fake-idle")
	p.m.SetState(client.MachineRunning)
	h := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake-idle", ID: "m-1", IdleStop: "2h"}}
	out, err := SetIdle(context.Background(), h)
	if err != nil || out != "powers off when idle" {
		t.Fatalf("SetIdle = %q, %v", out, err)
	}
	tr := p.m.T.(*clienttest.Transport)
	if r := tr.Remotes(); len(r) == 0 || !strings.HasSuffix(r[len(r)-1], " idle setup 2h") {
		t.Fatalf("remotes = %q", r)
	}
	if !PowersOffWhenIdle(Lima) || PowersOffWhenIdle(Docker) || PowersOffWhenIdle(E2B) {
		t.Fatal("only VMs power themselves off")
	}
	if _, err := SetIdle(context.Background(), client.Host{Name: "mine"}); err == nil {
		t.Fatal("a host without a machine has no idle stop")
	}
}

func TestSystemCreatePicksTheProvider(t *testing.T) {
	p := newFake(t, "fake-system")
	s := System{Providers: []provider.Provider{p}}
	if _, err := s.Create(context.Background(), "fake-system", "box", io.Discard); err != nil {
		t.Fatal(err)
	}
	var ue *client.UserError
	if _, err := s.Create(context.Background(), "lima", "vm", io.Discard); !errors.As(err, &ue) {
		t.Fatalf("an unknown provider: %v", err)
	}
	places := s.Places()
	if _, ok := FindPlace(places, Lima); ok {
		t.Fatal("lima offered by a system without it")
	}
}

func TestSystemSSH(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := System{}
	if s.PublicKey() != "" || s.HasSSHHost("my-server") {
		t.Fatal("an empty home has a key or a host")
	}
	_ = os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".ssh", "id_rsa.pub"), []byte("ssh-rsa AAAA test\n"), 0o644)
	_ = os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519.pub"), []byte("ssh-ed25519 AAAA test\n"), 0o644)
	if k := s.PublicKey(); k != "~/.ssh/id_ed25519.pub" {
		t.Fatalf("key = %q", k)
	}
	cfg := filepath.Join(home, ".ssh", "config")
	_ = os.WriteFile(cfg, []byte("Host old\n    User me"), 0o600)
	lines := RentPlan{Addr: "203.0.113.10", Name: "my-server"}.ConfigLines()
	if err := s.AddSSHHost(lines); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(cfg)
	if string(b) != "Host old\n    User me\n\n"+lines {
		t.Fatalf("config = %q", b)
	}
	if !s.HasSSHHost("my-server") || !s.HasSSHHost("old") {
		t.Fatal("the new host is not found")
	}
	if st, _ := os.Stat(cfg); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	// A new config is made private.
	_ = os.RemoveAll(filepath.Join(home, ".ssh"))
	if err := s.AddSSHHost(lines); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfg); string(b) != lines {
		t.Fatalf("new config = %q", b)
	}
}

// When the generated SSH config cannot be written, SSHWorks says which
// folder to fix, and runs no ssh.
func TestSSHWorksNeedsAConfigFolder(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder that this user cannot write to")
	}
	cfg := t.TempDir()
	if err := os.Chmod(cfg, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfg, 0o700) })
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("PATH", t.TempDir()) // no ssh at all
	err := System{}.SSHWorks(context.Background(), "ok")
	var ue *client.UserError
	dir := filepath.Join(client.ConfigDir(), "ssh")
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "cannot write the SSH config") || !strings.Contains(ue.Hint, dir) {
		t.Fatalf("err = %v, want a user error whose hint names %s", err, dir)
	}
}

func TestSSHWorks(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	argsLog := filepath.Join(bin, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + argsLog + "\n" +
		"case \"$*\" in *' -- ok true') exit 0;; esac\n" +
		"echo 'dev@203.0.113.10: Permission denied (publickey).' >&2\nexit 255\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s := System{}
	if err := s.SSHWorks(context.Background(), "ok"); err != nil {
		t.Fatalf("ok: %v", err)
	}
	// It forwards nothing, reads the generated config, and shares no
	// connection (decision 0034).
	cfg := filepath.Join(client.ConfigDir(), "ssh", "default")
	got, _ := os.ReadFile(argsLog)
	want := "-F " + cfg + " -o BatchMode=yes -o ConnectTimeout=15 -o ForwardAgent=no -o ForwardX11=no -o PermitLocalCommand=no" +
		" -o GSSAPIDelegateCredentials=no -o ClearAllForwardings=yes -o ControlMaster=no -o ControlPath=none -- ok true\n"
	if string(got) != want {
		t.Errorf("ssh ran with\n%q, want\n%q", got, want)
	}
	if b, err := os.ReadFile(cfg); err != nil || !strings.Contains(string(b), "\nForwardAgent no\nForwardX11 no\nPermitLocalCommand no\nGSSAPIDelegateCredentials no\n") {
		t.Errorf("the generated config = %q, %v, want the forwarding block", b, err)
	}
	err := s.SSHWorks(context.Background(), "dev@203.0.113.10")
	var ue *client.UserError
	if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "Permission denied (publickey)") || !strings.Contains(ue.Hint, "ssh dev@203.0.113.10") {
		t.Fatalf("err = %v", err)
	}
}
