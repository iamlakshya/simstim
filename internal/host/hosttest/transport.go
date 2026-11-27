package hosttest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
)

// Transport is a client.Transport to this machine. Start serves the real
// bridge, host.RunBridge, in this process over pipes, whatever the remote
// command says; Run runs the command with sh, here.
type Transport struct {
	mu      sync.Mutex
	remotes []string
	open    map[*bridgeProc]bool
	down    error
}

// Remotes lists the commands that Start and Run were given, in order.
func (t *Transport) Remotes() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.remotes...)
}

// Drop ends every open bridge connection at once, as a dropped network
// would. It returns how many it ended.
func (t *Transport) Drop() int {
	t.mu.Lock()
	procs := make([]*bridgeProc, 0, len(t.open))
	for p := range t.open {
		procs = append(procs, p)
	}
	t.mu.Unlock()
	for _, p := range procs {
		p.Kill()
	}
	return len(procs)
}

// Open counts the bridge connections that are open now.
func (t *Transport) Open() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.open)
}

// SetDown makes Start fail with err, as an unreachable host does, until it
// is called with nil.
func (t *Transport) SetDown(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.down = err
}

// ReachHint is fixed.
func (t *Transport) ReachHint() string { return "check the test host" }

// Run runs remote with sh on this machine.
func (t *Transport) Run(ctx context.Context, remote string, stdin io.Reader) ([]byte, error) {
	t.mu.Lock()
	t.remotes = append(t.remotes, remote)
	t.mu.Unlock()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
	cmd.Stdin = stdin
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil
}

// Start serves the bridge on the returned process's stdin and stdout. When
// ctx ends, the bridge is killed, as an SSH transport kills ssh.
func (t *Transport) Start(ctx context.Context, remote string) (client.Proc, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.remotes = append(t.remotes, remote)
	if t.down != nil {
		return nil, t.down
	}
	if strings.Contains(remote, "printf 'baseline-v1\\n'") {
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		p := &bridgeProc{in: inW, out: outR, done: make(chan struct{})}
		stop := context.AfterFunc(ctx, p.Kill)
		go func() {
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", remote)
			cmd.Stdin = inR
			cmd.Stdout = outW
			p.err = cmd.Run()
			stop()
			_ = outW.Close()
			_ = inR.Close()
			close(p.done)
		}()
		return p, nil
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &bridgeProc{in: inW, out: outR, done: make(chan struct{})}
	if t.open == nil {
		t.open = map[*bridgeProc]bool{}
	}
	t.open[p] = true
	stop := context.AfterFunc(ctx, p.Kill)
	go func() { //nolint:contextcheck // RunBridge has no context: like the real bridge, it runs until its input closes, and ctx closes it through Kill
		p.err = host.RunBridge(inR, outW)
		stop()
		_ = outW.Close()
		_ = inR.Close()
		t.mu.Lock()
		delete(t.open, p)
		t.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}

type bridgeProc struct {
	in   *io.PipeWriter
	out  *io.PipeReader
	done chan struct{}
	err  error
}

var errKilled = errors.New("killed")

func (p *bridgeProc) Stdin() io.WriteCloser { return p.in }
func (p *bridgeProc) Stdout() io.Reader     { return p.out }
func (p *bridgeProc) Stderr() string        { return "" }

func (p *bridgeProc) Wait() error {
	<-p.done
	if errors.Is(p.err, errKilled) {
		return p.err
	}
	return nil
}

func (p *bridgeProc) Kill() {
	_ = p.in.CloseWithError(errKilled)
	_ = p.out.CloseWithError(errKilled)
}

// machine is a client.Lifecycle that is always running, reached through a
// Transport.
type machine struct{ t *Transport }

func (machine) Status(context.Context) (string, error) { return client.MachineRunning, nil }
func (machine) Start(context.Context) error            { return nil }
func (machine) Stop(context.Context) error             { return nil }
func (machine) Destroy(context.Context) error          { return nil }
func (m machine) Transport(context.Context, bool) (client.Transport, error) {
	return m.t, nil
}

// Provider is the provider name that ClientHost's hosts use.
const Provider = "hosttest"

var (
	machinesMu sync.Mutex
	machines   = map[string]*Transport{}
	registered sync.Once
)

// ClientHost returns a client.Host named name that reaches this host through
// a new Transport, as a host made by `host create` would. The client finds
// it by its machine, so it needs no SSH.
func (h *Host) ClientHost(name string) (client.Host, *Transport) {
	registered.Do(func() {
		client.RegisterProvider(Provider, func(ch client.Host) (client.Lifecycle, error) {
			machinesMu.Lock()
			defer machinesMu.Unlock()
			t := machines[ch.Machine.ID]
			if t == nil {
				return nil, fmt.Errorf("no test machine %q", ch.Machine.ID)
			}
			return machine{t}, nil
		})
	})
	t := &Transport{}
	id := h.Root + "/" + name
	machinesMu.Lock()
	machines[id] = t
	machinesMu.Unlock()
	h.t.Cleanup(func() {
		t.Drop()
		machinesMu.Lock()
		delete(machines, id)
		machinesMu.Unlock()
	})
	return client.Host{Name: name, Machine: &client.Machine{Provider: Provider, ID: id}}, t
}
