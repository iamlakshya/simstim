//go:build unix

package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/provider"
)

// A Ctrl+C while a command waits for the lock of hosts.toml cancels it as
// at any other step: the usual line, exit code 130, nothing saved, and no
// .who file left (docs/contributing/cli-spec.md 5.1 and 11.3).
func TestASignalWhileWaitingForHostsToml(t *testing.T) {
	hosttest.New(t)
	if err := client.SaveHosts(context.Background(), []client.Host{{Name: "mine", SSH: "mine"}}); err != nil {
		t.Fatal(err)
	}
	// Another command holds the lock of hosts.toml, and does not let it go.
	f, err := os.OpenFile(filepath.Join(client.ConfigDir(), "hosts.toml.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainInAChild$")
	cmd.Env = append(os.Environ(), mainArgsEnv+"="+strings.Join([]string{"host", "rm", "mine"}, mainArgsSep), "NO_COLOR=1")
	var stderr lockedBuffer
	cmd.Stderr = &stderr
	proc := startChild(t, cmd)
	hosttest.WaitFor(t, 10*time.Second, "the wait for hosts.toml", func() bool {
		return strings.Contains(stderr.String(), "Waiting for another command that saves "+filepath.Join(client.ConfigDir(), "hosts.toml")+"…\n")
	})
	signalled := time.Now()
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	// The cancel ends the wait at once, long before the wait's own end of
	// 10 seconds, with the line of any signal.
	code := exitOf(t, proc)
	_, said, _ := strings.Cut(stderr.String(), "…\n")
	if took := time.Since(signalled); code != 130 || took > 5*time.Second ||
		said != "Error: the command stopped, because it got a signal to end.\n\nTry: Run the command again.\n" {
		t.Errorf("exit %d after %s, stderr:\n%s", code, took, stderr.String())
	}
	if who, _ := filepath.Glob(filepath.Join(client.StateDir(), "hosts", "*.who")); len(who) != 0 {
		t.Errorf("a .who file stayed: %v", who)
	}
	if _, err := client.FindHost("mine"); err != nil {
		t.Error("the cancelled host rm forgot the host")
	}
}

// holdHostsToml takes the lock of hosts.toml, as another command that
// saves it and does not let it go, until the test ends.
func holdHostsToml(t *testing.T) func() {
	t.Helper()
	if err := os.MkdirAll(client.ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(client.ConfigDir(), "hosts.toml.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	release := sync.OnceFunc(func() { _ = f.Close() })
	t.Cleanup(release)
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	return release
}

type lockAfterCreateProvider struct {
	provider.Provider
	t       *testing.T
	created bool
}

func (p *lockAfterCreateProvider) Create(ctx context.Context, req provider.Request) (client.Host, error) {
	h, err := p.Provider.Create(ctx, req)
	if err == nil {
		p.created = true
		holdHostsToml(p.t)
	}
	return h, err
}

// When host create's save times out while hosts.toml is held after provider
// creation, it does not auto-destroy the unsaved machine: it reports the
// exact provider resource that may remain and saves nothing to hosts.toml.
func TestCreateDeletesTheMachineThatItCouldNotSave(t *testing.T) {
	m, _ := withFake(t, nil)
	defer client.ShortenHostsWait(200 * time.Millisecond)()
	p, _ := findProvider("fake")
	wp := &lockAfterCreateProvider{Provider: p, t: t}
	err := createHost(context.Background(), wp, quietRequest("box"))
	if !wp.created {
		t.Fatalf("provider Create did not run before save timeout: %+v", err)
	}
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "hosts_busy" || !strings.Contains(ue.Msg, "fake-box") || !strings.Contains(ue.Msg, "may remain") || ue.Hint == "" {
		t.Fatalf("host create whose save waited out its time: %+v, calls %v", err, m.Calls())
	}
	if len(m.Calls()) != 0 {
		t.Fatalf("timed-out create unexpectedly called provider destroy: %v", m.Calls())
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Error("a host was saved")
	}
}
