package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
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
func holdHostsToml(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(client.ConfigDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(client.ConfigDir(), "hosts.toml.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
}

// host create never leaves a machine that no saved host names: when the
// save waits out its time, it deletes the machine that it made. When that
// delete fails too, the error names the machine that stays, and the tool
// to delete it with.
func TestCreateDeletesTheMachineThatItCouldNotSave(t *testing.T) {
	m, _ := withFake(t, nil)
	holdHostsToml(t)
	defer client.ShortenHostsWait(200 * time.Millisecond)()
	p, _ := findProvider("fake")
	err := createHost(context.Background(), p, quietRequest("box"))
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "hosts_busy" || !strings.HasSuffix(ue.Msg, ", so the fake machine fake-box was deleted again") ||
		!strings.HasPrefix(ue.Hint, "End that command") || !slices.Contains(m.Calls(), "destroy") {
		t.Fatalf("host create whose save waited out its time: %+v, calls %v", err, m.Calls())
	}
	m.SetState(client.MachineRunning)
	m.DestroyErr = errors.New("the daemon is gone")
	err = createHost(context.Background(), p, quietRequest("box"))
	if !errors.As(err, &ue) || !strings.HasSuffix(ue.Msg, ", and the fake machine fake-box stays: the daemon is gone") ||
		ue.Hint != "Delete it with fake's own tools, then run the command again." {
		t.Fatalf("host create whose delete failed too: %+v", err)
	}
	if _, err := client.FindHost("box"); err == nil {
		t.Error("a host was saved")
	}
}
