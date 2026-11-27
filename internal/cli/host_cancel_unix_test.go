//go:build unix

package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

func observationChild(t *testing.T, stage string, term *hosttest.Terminal, args ...string) (*observationProcess, *bytes.Buffer, *lockedBuffer, string) {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHostObservationChild$")
	cmd.Env = append(os.Environ(), observationEnv+"="+stage+":"+dir, mainArgsEnv+"="+strings.Join(args, mainArgsSep), "NO_COLOR=1", "TERM=xterm-256color")
	out, errOut := &bytes.Buffer{}, &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = out, errOut
	if term != nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = term.TTY, term.TTY, term.TTY
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child := &observationProcess{Cmd: cmd, done: make(chan struct{})}
	go func() {
		child.err = cmd.Wait()
		close(child.done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-child.done:
		case <-time.After(5 * time.Second):
			t.Error("the observation child did not end")
		}
	})
	return child, out, errOut, dir
}
func saveObservationHost(t *testing.T) {
	t.Helper()
	hosttest.New(t)
	h := client.Host{Name: "box", Machine: &client.Machine{Provider: "observation", ID: "test-box", Created: "1"}}
	if err := client.SaveHosts(context.Background(), []client.Host{h}); err != nil {
		t.Fatal(err)
	}
}
func observationExit(t *testing.T, child *observationProcess) int {
	t.Helper()
	select {
	case <-child.done:
	case <-time.After(20 * time.Second):
		t.Fatal("the observation child did not end after the signal")
	}
	if child.ProcessState == nil {
		t.Fatalf("wait for the observation child: %v", child.err)
	}
	return child.ProcessState.ExitCode()
}
func signalDuringObservation(t *testing.T, cmd *observationProcess, dir string) {
	t.Helper()
	hosttest.WaitFor(t, 10*time.Second, "the observation to begin", func() bool {
		_, err := os.Stat(filepath.Join(dir, "ready"))
		return err == nil
	})
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if code := observationExit(t, cmd); code != 130 {
		t.Errorf("cancelled observation exited with %d", code)
	}
}
func TestSignalDuringHostRmObservation(t *testing.T) {
	for _, stage := range []string{"state", "bridge"} {
		for _, mode := range []string{"destroy", "force", "interactive"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				saveObservationHost(t)
				args := []string{"host", "rm", "box"}
				var term *hosttest.Terminal
				if mode == "interactive" {
					term = hosttest.NewTerminal(t, 100, 30)
				} else {
					args = append(args, "--destroy", "--confirm", "box")
					if mode == "force" {
						args = append(args, "--force")
					}
				}
				cmd, out, errOut, dir := observationChild(t, stage, term, args...)
				signalDuringObservation(t, cmd, dir)
				said := out.String() + errOut.String()
				if term != nil {
					said = string(term.WaitFor("Try: "))
				}
				if strings.Contains(said, "--force") || strings.Contains(said, "Type box") || strings.Contains(said, "sessions could not be listed") || strings.Contains(said, "Forgot") || strings.Contains(said, "Deleted") {
					t.Errorf("continued after cancellation: %q", said)
				}
				if _, err := os.Stat(filepath.Join(dir, "destroyed")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("called Destroy after cancellation: %v", err)
				}
				if _, err := client.FindHost("box"); err != nil {
					t.Errorf("forgot the host after cancellation: %v", err)
				}
			})
		}
	}
}
func TestSignalDuringHostShowWritesNoStdout(t *testing.T) {
	for _, json := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "json"}[json], func(t *testing.T) {
			saveObservationHost(t)
			args := []string{"host", "show", "box"}
			if json {
				args = append(args, "--json")
			}
			cmd, out, errOut, dir := observationChild(t, "bridge", nil, args...)
			signalDuringObservation(t, cmd, dir)
			if out.Len() != 0 || !strings.Contains(errOut.String(), "signal to end") {
				t.Errorf("stdout %q, stderr %q", out.String(), errOut.String())
			}
		})
	}
}
func TestSignalAfterDestroyReportsTheCompletedStep(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "non-interactive retry", true: "interactive retry"}[interactive], func(t *testing.T) {
			saveObservationHost(t)
			cmd, out, errOut, dir := observationChild(t, "destroy", nil, "host", "rm", "box", "--destroy", "--confirm", "box")
			hosttest.WaitFor(t, 10*time.Second, "the machine deletion", func() bool {
				_, err := os.Stat(filepath.Join(dir, "destroyed"))
				return err == nil
			})
			releaseHosts := holdHostsToml(t)
			if err := os.WriteFile(filepath.Join(dir, "release-destroy"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			hosttest.WaitFor(t, 10*time.Second, "the wait after deletion", func() bool {
				return strings.Contains(errOut.String(), "Waiting for another command that saves ")
			})
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
			if code := observationExit(t, cmd); code != 130 || out.Len() != 0 || !strings.Contains(errOut.String(), "Cancelled. Done before that: deleted the observation machine test-box.") {
				t.Errorf("exit %d, stdout %q, stderr %q", code, out.String(), errOut.String())
			}
			if _, err := os.Stat(filepath.Join(dir, "destroyed")); err != nil {
				t.Errorf("no completed deletion: %v", err)
			}
			saved, err := client.FindHost("box")
			if err != nil {
				t.Fatalf("the cancelled forget removed the saved entry: %v", err)
			}
			if rec := client.LoadRemoval(saved); !rec.Has(client.RemovalDestroy) {
				t.Fatal("the completed deletion has no retry receipt")
			}
			if who, _ := filepath.Glob(filepath.Join(client.StateDir(), "hosts", "*.who")); len(who) != 0 {
				t.Fatalf("the cancelled command left lock owners: %v", who)
			}
			releaseHosts()
			var terminal *hosttest.Terminal
			if interactive {
				terminal = hosttest.NewTerminal(t, 100, 30)
			}
			retry, retryOut, retryErr, _ := observationChild(t, "retry", terminal, "host", "rm", "box")
			if code := observationExit(t, retry); code != 0 {
				t.Fatalf("retry exited %d: %s%s", code, retryOut.String(), retryErr.String())
			}
			said := retryOut.String() + retryErr.String()
			if terminal != nil {
				said = string(terminal.WaitFor("Forgot box."))
			}
			if !strings.Contains(said, "was deleted by an earlier run") {
				t.Fatalf("retry did not reconcile the completed deletion: %q", said)
			}
			if _, err := client.FindHost("box"); err == nil {
				t.Fatal("retry left the host saved")
			}
			receipt := filepath.Join(client.StateDir(), "hosts", client.HostFileKey("box"), "removal.json")
			if _, err := os.Stat(receipt); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("retry left the removal receipt: %v", err)
			}
			if who, _ := filepath.Glob(filepath.Join(client.StateDir(), "hosts", "*.who")); len(who) != 0 {
				t.Fatalf("retry left lock owners: %v", who)
			}
		})
	}
}
