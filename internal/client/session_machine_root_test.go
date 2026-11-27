package client

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
)

// The root fixture models physical transport ownership without executing sudo,
// starting a process or opening a network listener.
type sessionMachineRootInputFixtureV1 struct {
	mu     sync.Mutex
	bytes  strings.Builder
	closed chan struct{}
	once   sync.Once
}

func (i *sessionMachineRootInputFixtureV1) Write(data []byte) (int, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.bytes.Write(data)
}
func (i *sessionMachineRootInputFixtureV1) Close() error {
	i.once.Do(func() { close(i.closed) })
	return nil
}
func (i *sessionMachineRootInputFixtureV1) text() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.bytes.String()
}

type sessionMachineRootProcFixtureV1 struct {
	input                    *sessionMachineRootInputFixtureV1
	waiting, killed, release chan struct{}
	once                     sync.Once
}

func (p *sessionMachineRootProcFixtureV1) Stdin() io.WriteCloser { return p.input }
func (*sessionMachineRootProcFixtureV1) Stdout() io.Reader {
	return strings.NewReader("__ROOT_STEP_DONE__\n")
}
func (*sessionMachineRootProcFixtureV1) Stderr() string { return "" }
func (p *sessionMachineRootProcFixtureV1) Wait() error {
	close(p.waiting)
	<-p.input.closed
	if p.release != nil {
		<-p.release
	}
	return nil
}
func (p *sessionMachineRootProcFixtureV1) Kill() {
	p.once.Do(func() { close(p.killed) })
	_ = p.input.Close()
}

type sessionMachineRootTransportFixtureV1 struct {
	bridge  Transport
	process *sessionMachineRootProcFixtureV1
	remote  string
}

func (t *sessionMachineRootTransportFixtureV1) Start(ctx context.Context, remote string) (Proc, error) {
	if remote == "sudo -n /bin/sh -s" {
		t.remote = remote
		return t.process, nil
	}
	return t.bridge.Start(ctx, remote)
}
func (*sessionMachineRootTransportFixtureV1) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, errors.New("unexpected root fixture Run")
}
func (*sessionMachineRootTransportFixtureV1) ReachHint() string { return "root-fixture" }

func TestSessionMachineRootUsesExactPasswordlessTransportAndJoinsCancellation(t *testing.T) {
	owner, _ := beginSessionMachineFixtureV1(t, false)
	probe := prepareGVisorProbe()
	probe.Sudo, probe.SudoPasswordless = true, true
	bridge := newPrepareBridgeTransport(t, strings.Repeat("8", 32), []string{"sandbox", "session.id"}, func(request proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
		if request.Op == "sandbox.probe" {
			return probe, nil, false
		}
		return nil, &proto.Error{Code: "unknown", Message: request.Op}, false
	})
	process := &sessionMachineRootProcFixtureV1{input: &sessionMachineRootInputFixtureV1{closed: make(chan struct{})}, waiting: make(chan struct{}), killed: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(process.release) }) }
	t.Cleanup(release)
	transport := &sessionMachineRootTransportFixtureV1{bridge: bridge, process: process}
	conn, err := owner.Target().dialRoute(owner.Context(t.Context()), route{t: transport, machineOwner: owner})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := NewRootStepGrantRequest()
	ctx, cancel := context.WithCancel(WithGrantRequest(owner.Context(t.Context()), request))
	defer cancel()
	script, err := BuildRootScript(probe.User, probe.UID, []RootStepID{RootLinger}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.CheckRootStepGrant(ctx, script, RootRun, true); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		done <- conn.RunRootStep(ctx, script, RootRun, RootRunOptions{Passwordless: true, Out: io.Discard})
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		process.Kill()
		release()
		select {
		case <-finished:
			if err := request.Close(); err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("root fixture cleanup did not join its original execution")
		}
	})
	select {
	case <-process.waiting:
	case err := <-done:
		t.Fatalf("root did not start: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("root transport did not start")
	}
	cancel()
	select {
	case <-process.killed:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not kill the original process")
	}
	select {
	case err := <-done:
		t.Fatalf("cancellation fabricated process exit: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("joined root cancellation = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("original root process was not joined")
	}
	if transport.remote != "sudo -n /bin/sh -s" || process.input.text() != script.Text {
		t.Fatal("root execution changed its fixed command or script stdin")
	}
	if err := request.Close(); err != nil {
		t.Fatalf("joined root execution retained active settings: %v", err)
	}
}
