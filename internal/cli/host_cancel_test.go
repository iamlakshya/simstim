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
