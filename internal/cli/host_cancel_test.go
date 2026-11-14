package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

const observationEnv = "CLI_TEST_OBSERVATION"

type observationMachine struct {
	*clienttest.Machine
	stage, dir string
}

func (m observationMachine) Status(ctx context.Context) (string, error) {
	if m.stage == "state" {
		return "", waitForObservationCancel(ctx, m.dir)
	}
	return m.Machine.Status(ctx)
}

func (m observationMachine) Destroy(ctx context.Context) error {
	if err := m.Machine.Destroy(ctx); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(m.dir, "destroyed"), nil, 0o600); err != nil {
		return err
	}
	if m.stage != "destroy" {
		return nil
	}
	// The parent takes the save lock only after the actual delete, so
	// its signal observes the forget rather than the initial entry check.
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(filepath.Join(m.dir, "release-destroy")); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

type observationTransport struct {
	*clienttest.Transport
	dir string
}

// Start returns immediately, as a transport must. The proxy forwards the
// bridge protocol but holds its ls response, so the signal comes during
// the actual sessions request rather than during process creation.
func (tr observationTransport) Start(ctx context.Context, remote string) (client.Proc, error) {
	p, err := tr.Transport.Start(ctx, remote)
	if err != nil {
		return nil, err
	}
	r, w := io.Pipe()
	go func() {
		defer r.Close()
		defer p.Stdin().Close()
		reader, writer := bufio.NewReader(r), proto.NewWriter(p.Stdin())
		for {
			f, err := proto.Read(reader)
			if err != nil {
				return
			}
			var req proto.Request
			if f.Type == proto.TRequest && json.Unmarshal(f.Payload, &req) == nil && req.Op == "ls" {
				if err := os.WriteFile(filepath.Join(tr.dir, "ready"), nil, 0o600); err != nil {
					return
				}
				continue
			}
			if err := writer.Write(f.Type, f.Payload); err != nil {
				return
			}
		}
	}()
	return observationProc{Proc: p, in: w}, nil
}

type observationProc struct {
	client.Proc
	in *io.PipeWriter
}

func (p observationProc) Stdin() io.WriteCloser { return p.in }
func (p observationProc) Kill() {
	_ = p.in.Close()
	p.Proc.Kill()
}

func waitForObservationCancel(ctx context.Context, dir string) error {
	if err := os.WriteFile(filepath.Join(dir, "ready"), nil, 0o600); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

// The child uses only an in-memory machine. Its markers prove both that
// the signal arrived during observation and that Destroy was never called.
func TestHostObservationChild(t *testing.T) {
	stage, dir, ok := strings.Cut(os.Getenv(observationEnv), ":")
	if !ok {
		return
	}
	tr := &clienttest.Transport{}
	var transport client.Transport = tr
	if stage == "bridge" {
		transport = observationTransport{Transport: tr, dir: dir}
	}
	state := client.MachineRunning
	if stage == "retry" {
		state = client.MachineMissing
	}
	m := observationMachine{Machine: clienttest.NewMachine(state, transport), stage: stage, dir: dir}
	client.RegisterProvider("observation", func(client.Host) (client.Lifecycle, error) { return m, nil })
	os.Args = append([]string{"client"}, strings.Split(os.Getenv(mainArgsEnv), mainArgsSep)...)
	os.Exit(Main())
}

type observationProcess struct {
	*exec.Cmd
	done chan struct{}
	err  error
}

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

func TestHostRmSecondLookKeepsCancellation(t *testing.T) {
	fm, _ := withCreated(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := afterAnswer
	afterAnswer = func(string) { cancel() }
	t.Cleanup(func() { afterAnswer = old })
	var out bytes.Buffer
	p := testPrompts("box\n", &out)
	p.ctx = ctx
	err := removeHost(ctx, p, rmOpts{name: "box"})
	if !errors.Is(err, context.Canceled) || fm.destroyed() {
		t.Errorf("second look after cancellation: %v, destroyed %v", err, fm.destroyed())
	}
}

type cancellingStatus struct {
	client.Lifecycle
	cancel context.CancelFunc
}

func (lc cancellingStatus) Status(context.Context) (string, error) {
	lc.cancel()
	return client.MachineMissing, nil
}

func TestHostRmCancelledSecondObservationDoesNotRecordOrPrint(t *testing.T) {
	fm, h := withCreated(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := rmMachine{h: h, lc: cancellingStatus{Lifecycle: fm.Machine, cancel: cancel}}
	rec := client.LoadRemoval(h)
	var out bytes.Buffer
	p := testPrompts("", &out)
	p.ctx = ctx
	if destroy, err := m.lookAgain(ctx, p, rmOpts{name: h.Name}, &rec); destroy || !errors.Is(err, context.Canceled) || rec.Has(client.RemovalDestroy) || out.Len() != 0 {
		t.Errorf("cancelled second observation: destroy %v, error %v, recorded %v, output %q", destroy, err, rec.Has(client.RemovalDestroy), out.String())
	}
}
