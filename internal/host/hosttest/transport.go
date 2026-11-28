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
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &bridgeProc{in: inW, out: outR, done: make(chan struct{})}
	if t.open == nil {
		t.open = map[*bridgeProc]bool{}
	}
	t.open[p] = true
	stop := context.AfterFunc(ctx, p.Kill)
	go func() { //nolint:contextcheck // RunBridge and friends have no context: like the real helper, they run until their input closes, and ctx closes them through Kill
		p.err = runHelperInProcess(remote, inR, outW)
		if p.err != nil {
			p.stderr = p.err.Error()
		}
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

// runHelperInProcess dispatches the helper's streaming subcommands in this
// process over pipes, matching what the real host helper does over SSH.
func runHelperInProcess(remote string, in io.Reader, out io.Writer) error {
	args := splitRemoteArgs(remote)
	if len(args) >= 3 {
		switch args[1] {
		case "sftp":
			if len(args) >= 4 && args[2] == "--stage" {
				return host.RunStageSFTP(args[3], in, out)
			}
			return host.RunSFTP(args[2], in, out)
		case "ssh-exec":
			return host.RunSSHExec(args[2], in, out)
		case "dial-port":
			return host.RunDialPort(args[2:], in, out)
		case "pull-bundle":
			return host.RunPullBundle(args[2], in, out)
		}
	}
	return host.RunBridge(in, out)
}

// splitRemoteArgs parses whitespace-separated tokens from a POSIX shell
// command line with optional single-quoted words (as shellWord produces).
func splitRemoteArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inToken, inSingle := false, false
	for i := range len(s) {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
			}
		case c == '\'':
			inSingle = true
			inToken = true
		case c == ' ' || c == '\t' || c == '\n':
			if inToken {
				out = append(out, cur.String())
				cur.Reset()
				inToken = false
			}
		default:
			cur.WriteByte(c)
			inToken = true
		}
	}
	if inToken {
		out = append(out, cur.String())
	}
	return out
}

type bridgeProc struct {
	in     *io.PipeWriter
	out    *io.PipeReader
	done   chan struct{}
	err    error
	stderr string
}

var errKilled = errors.New("killed")

func (p *bridgeProc) Stdin() io.WriteCloser { return p.in }
func (p *bridgeProc) Stdout() io.Reader     { return p.out }
func (p *bridgeProc) Stderr() string {
	select {
	case <-p.done:
		return p.stderr
	default:
		return ""
	}
}

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
