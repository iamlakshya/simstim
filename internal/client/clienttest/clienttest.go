// Package clienttest has fakes for tests of code that reaches hosts: a
// transport that serves a small bridge in the same process, and a machine
// whose lifecycle is kept in memory. Only tests import it.
package clienttest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// Transport serves a fake bridge for Start, and answers the probe for Run.
type Transport struct {
	// Sessions is what the bridge's ls returns.
	Sessions []client.Session
	// Every, if set, makes the transport a client.KeepAliver.
	Every time.Duration
	// Agents is what the bridge's agents.check returns.
	Agents []api.AgentCheck
	// Silent makes the bridge read the client's hello and never answer, as
	// a stuck host does.
	Silent bool

	mu      sync.Mutex
	remotes []string
	pings   int
}

// Remotes lists the commands that Start and Run were given.
func (t *Transport) Remotes() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.remotes...)
}

// SetAgents changes what agents.check returns, as an install would.
func (t *Transport) SetAgents(a []api.AgentCheck) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Agents = a
}

// Pings counts the pings the bridge received.
func (t *Transport) Pings() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pings
}

func (t *Transport) record(remote string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.remotes = append(t.remotes, remote)
}

// ReachHint is fixed.
func (t *Transport) ReachHint() string { return "check the fake" }

// Run answers the bootstrap probe as a Linux host with the helper installed.
func (t *Transport) Run(_ context.Context, remote string, stdin io.Reader) ([]byte, error) {
	t.record(remote)
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	switch {
	case strings.Contains(remote, "uname -sm"):
		return []byte("Linux aarch64\n__HELPER_PRESENT__\n"), nil
	case strings.Contains(remote, " idle setup "):
		return []byte("powers off when idle\n"), nil
	}
	return nil, nil
}

// Start serves the fake bridge on the returned process's stdio.
func (t *Transport) Start(_ context.Context, remote string) (client.Proc, error) {
	t.record(remote)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &pipeProc{in: inW, out: outR, done: make(chan struct{})}
	go func() {
		defer close(p.done)
		t.serve(inR, outW)
		_ = outW.Close()
		_ = inR.Close()
	}()
	return p, nil
}

// serve speaks just enough of the bridge's protocol: hello, ping, ls,
// setup, doctor and agents.check.
func (t *Transport) serve(in io.Reader, out io.Writer) {
	r := bufio.NewReader(in)
	w := proto.NewWriter(out)
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.THello {
		return
	}
	if t.Silent {
		_, _ = io.Copy(io.Discard, r) // until the client kills it
		return
	}
	_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge"})
	for {
		f, err := proto.Read(r)
		if err != nil {
			return
		}
		switch f.Type {
		case proto.TPing:
			t.mu.Lock()
			t.pings++
			t.mu.Unlock()
			_ = w.Write(proto.TPong, nil)
		case proto.TRequest:
			var req proto.Request
			_ = json.Unmarshal(f.Payload, &req)
			_ = w.WriteJSON(proto.TResponse, t.answer(req))
		default:
			// The fake bridge answers pings and requests only.
		}
	}
}

func (t *Transport) answer(req proto.Request) proto.Response {
	var v any
	switch req.Op {
	case "ls":
		sessions := t.Sessions
		if sessions == nil {
			sessions = []client.Session{}
		}
		v = sessions
	case "setup":
		v = api.SetupResult{Steps: []api.Check{{Name: "linger", OK: true, Detail: "already on"}}}
	case "doctor":
		v = api.HostInfo{Distro: "Fake Linux", CPUs: 2, MemTotal: 4 << 30, Agents: map[string]string{"shell": "/bin/sh"}}
	case "agents.check":
		t.mu.Lock()
		agents := append([]api.AgentCheck{}, t.Agents...)
		t.mu.Unlock()
		v = agents
	default:
		return proto.Response{ID: req.ID, Error: &proto.Error{Code: "unknown_op", Message: "unknown request " + req.Op}}
	}
	b, _ := json.Marshal(v)
	return proto.Response{ID: req.ID, OK: true, Result: b}
}

type pipeProc struct {
	in   *io.PipeWriter
	out  *io.PipeReader
	done chan struct{}
}

func (p *pipeProc) Stdin() io.WriteCloser { return p.in }
func (p *pipeProc) Stdout() io.Reader     { return p.out }
func (p *pipeProc) Stderr() string        { return "" }
func (p *pipeProc) Wait() error           { <-p.done; return nil }
func (p *pipeProc) Kill() {
	_ = p.in.CloseWithError(errors.New("killed"))
	_ = p.out.CloseWithError(errors.New("killed"))
}

// KeepAliveTransport is a Transport that asks the client to ping.
type KeepAliveTransport struct{ *Transport }

// KeepAlive is the fake's ping period.
func (k KeepAliveTransport) KeepAlive() time.Duration { return k.Every }

// Machine is a fake machine: its state lives in memory, and every call is
// recorded.
type Machine struct {
	T client.Transport

	mu      sync.Mutex
	state   string
	calls   []string
	renewed []time.Duration
	// Busy and Idle are the leases, for LeasedMachine.
	Busy, Idle time.Duration
}

// NewMachine returns a machine in the given state.
func NewMachine(state string, t client.Transport) *Machine {
	return &Machine{state: state, T: t}
}

func (m *Machine) call(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, name)
}

// Calls lists the lifecycle calls so far, in order.
func (m *Machine) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

// Renewed lists the leases asked for.
func (m *Machine) Renewed() []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]time.Duration(nil), m.renewed...)
}

// SetState sets the state that Status reports.
func (m *Machine) SetState(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = s
}

// Status reports the state.
func (m *Machine) Status(context.Context) (string, error) {
	m.call("status")
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state, nil
}

// Start makes it running.
func (m *Machine) Start(context.Context) error {
	m.call("start")
	m.SetState(client.MachineRunning)
	return nil
}

// Stop makes it stopped.
func (m *Machine) Stop(context.Context) error {
	m.call("stop")
	m.SetState(client.MachineStopped)
	return nil
}

// Destroy makes it missing.
func (m *Machine) Destroy(context.Context) error {
	m.call("destroy")
	m.SetState(client.MachineMissing)
	return nil
}

// Transport returns T.
func (m *Machine) Transport(context.Context, bool) (client.Transport, error) {
	m.call("transport")
	return m.T, nil
}

// LeasedMachine is a Machine that is also a client.Leaser.
type LeasedMachine struct{ *Machine }

// Renew records the lease.
func (l LeasedMachine) Renew(_ context.Context, d time.Duration) error {
	l.call("renew")
	l.mu.Lock()
	defer l.mu.Unlock()
	l.renewed = append(l.renewed, d)
	return nil
}

// Leases are Busy and Idle.
func (l LeasedMachine) Leases() (busy, idle time.Duration) { return l.Busy, l.Idle }
