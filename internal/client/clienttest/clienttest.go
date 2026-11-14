// Package clienttest has fakes for tests of code that reaches hosts: a
// transport that serves a small bridge in the same process, and a machine
// whose lifecycle is kept in memory. Only tests import it.
package clienttest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

// WriteScript writes an executable script at path, for a test to run as a
// fake program. Another process writes it, so this one never holds the
// file open for writing: a process that this one starts at that moment
// would keep the open file, and the script's exec would fail with "text
// file busy".
func WriteScript(t testing.TB, path, body string) {
	t.Helper()
	src := path + ".src"
	if err := os.WriteFile(src, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "cp", src, path).CombinedOutput(); err != nil {
		t.Fatalf("write the script %s: %v: %s", path, err, out)
	}
}

// Transport serves a fake bridge for Start, and answers the probe for Run.
type Transport struct {
	// Screens are the bytes that peek returns for each exact session ID.
	Screens map[string][]byte
	// NewID overrides every successful new response for hostile-ID fixtures.
	// Without it the bridge returns distinct legacy IDs fake1, fake2 and so on.
	NewID string
	// BridgeStderr makes the owned fake process fail before its hello.
	BridgeStderr string
	// Replies overrides successful payloads, including an explicit null.
	Replies map[string]any
	// Sessions is what the bridge's ls returns.
	Sessions []client.Session
	// Every, if set, makes the transport a client.KeepAliver.
	Every time.Duration
	// Agents is what the bridge's agents.check returns.
	Agents []api.AgentCheck
	// Answers, when set, are what the bridge returns for requests by their
	// op, such as "ports", in place of the answers above.
	Answers map[string]any
	// Features are the features that the bridge's hello offers.
	Features []string
	// HostID is the host ID in the bridge's hello, or a fixed valid ID when
	// empty and NoHostID is false.
	HostID string
	// NoHostID makes the bridge omit host_id in its hello, as an older
	// helper did.
	NoHostID bool
	// Uname is what the probe's uname -sm prints: "Linux aarch64" when empty.
	Uname string
	// fail makes the bridge answer a request with an error, by its op.
	fail map[string]*proto.Error
	// NoHelper makes the probe answer as a host without the helper.
	NoHelper bool
	// Helper, if set, is the one version of the helper on the host. A
	// bridge of another version does not start, as when its file is
	// missing, and the probe finds only this one.
	Helper string
	// OnRun, if set, gets each command that Run is given, before Run
	// answers it.
	OnRun func(remote string)
	// Steps, if set, is what the bridge's setup returns.
	Steps []api.Check
	// OnRequest, if set, gets the op of each request that reaches the
	// bridge, before the bridge answers it.
	OnRequest func(op string)
	// Fixes is what doctor.fix lists, and applies as done.
	Fixes []api.Fix

	mu       sync.Mutex
	remotes  []string
	pings    int
	requests []string
	ops      []string
}

// Requests lists the ops of the requests that reached the bridge, in
// order.
func (t *Transport) Requests() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.requests...)
}

// Remotes lists the commands that Start and Run were given.
func (t *Transport) Remotes() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.remotes...)
}

// Ops lists the op of each request that the bridge received, in order,
// such as "agents.check" or "new".
func (t *Transport) Ops() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.ops...)
}

// SetSessions changes what the bridge's ls returns.
func (t *Transport) SetSessions(s []client.Session) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Sessions = s
}

// Fail makes the bridge answer every request of op with e, an error such
// as a host sends. A nil e answers op again.
func (t *Transport) Fail(op string, e *proto.Error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fail == nil {
		t.fail = map[string]*proto.Error{}
	}
	t.fail[op] = e
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

// Run answers the bootstrap probe as a Linux host with the helper installed,
// or without it when NoHelper is set, and takes an install.
func (t *Transport) Run(_ context.Context, remote string, stdin io.Reader) ([]byte, error) {
	t.record(remote)
	if t.OnRun != nil {
		t.OnRun(remote)
	}
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	switch {
	case strings.Contains(remote, "uname -sm") && (t.NoHelper || !t.has(remote)):
		return []byte("Linux aarch64\n"), nil
	case strings.Contains(remote, "uname -sm"):
		uname := t.Uname
		if uname == "" {
			uname = "Linux aarch64"
		}
		return []byte(uname + "\n__HELPER_PRESENT__\n"), nil
	case strings.Contains(remote, "echo __INSTALLED__"):
		return []byte("__INSTALLED__\n"), nil
	case strings.Contains(remote, " idle setup "):
		return []byte("powers off when idle\n"), nil
	}
	return nil, nil
}

// Start serves the fake bridge on the returned process's stdio.
func (t *Transport) Start(_ context.Context, remote string) (client.Proc, error) {
	t.record(remote)
	if !t.has(remote) {
		return missingProc{}, nil
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &pipeProc{in: inW, out: outR, done: make(chan struct{}), stderr: t.BridgeStderr}
	if t.BridgeStderr != "" {
		go func() {
			defer close(p.done)
			_ = outW.Close()
			_, _ = io.Copy(io.Discard, inR)
		}()
		return p, nil
	}
	go func() {
		defer close(p.done)
		t.serve(inR, outW)
		_ = outW.Close()
		_ = inR.Close()
	}()
	return p, nil
}

// serve speaks just enough of the bridge's protocol: hello, ping, ls,
// setup, doctor, agents.check and doctor.fix.
func (t *Transport) serve(in io.Reader, out io.Writer) {
	r := bufio.NewReader(in)
	w := proto.NewWriter(out)
	f, err := proto.Read(r)
	if err != nil || f.Type != proto.THello {
		return
	}
	hostID := t.HostID
	if hostID == "" && !t.NoHostID {
		hostID = "0123456789abcdef0123456789abcdef"
	}
	_ = w.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge", HostID: hostID, Features: t.Features})
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
			t.mu.Lock()
			t.requests = append(t.requests, req.Op)
			t.mu.Unlock()
			if t.OnRequest != nil {
				t.OnRequest(req.Op)
			}
			_ = w.WriteJSON(proto.TResponse, t.answer(req))
		default:
			// The fake bridge answers pings and requests only.
		}
	}
}

func (t *Transport) answer(req proto.Request) proto.Response {
	t.mu.Lock()
	t.ops = append(t.ops, req.Op)
	started := 0
	for _, op := range t.ops {
		if op == "new" {
			started++
		}
	}
	fail := t.fail[req.Op]
	sessions := append([]client.Session{}, t.Sessions...)
	newID := t.NewID
	reply, overridden := t.Replies[req.Op]
	answer, answered := t.Answers[req.Op]
	agents := append([]api.AgentCheck{}, t.Agents...)
	steps := append([]api.Check(nil), t.Steps...)
	fixes := append([]api.Fix{}, t.Fixes...)
	t.mu.Unlock()
	if fail != nil {
		return proto.Response{ID: req.ID, Error: fail}
	}
	if overridden {
		b, _ := json.Marshal(reply)
		return proto.Response{ID: req.ID, OK: true, Result: b}
	}
	if answered {
		b, _ := json.Marshal(answer)
		return proto.Response{ID: req.ID, OK: true, Result: b}
	}
	var v any
	switch req.Op {
	case "ls":
		v = sessions
	case "setup":
		v = api.SetupResult{Steps: []api.Check{{Name: "linger", OK: true, Detail: "already on"}}}
		if steps != nil {
			v = api.SetupResult{Steps: steps}
		}
	case "peek":
		var a struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(req.Args, &a)
		t.mu.Lock()
		data := append([]byte(nil), t.Screens[a.ID]...)
		t.mu.Unlock()
		v = map[string]any{"data": data, "cols": 80, "rows": 24}
	case "doctor":
		v = api.HostInfo{Distro: "Fake Linux", CPUs: 2, MemTotal: 4 << 30, Agents: map[string]string{"shell": "/bin/sh"}}
	case "agents.check":
		v = agents
	case "doctor.fix":
		var a api.FixArgs
		_ = json.Unmarshal(req.Args, &a)
		for i := range fixes {
			fixes[i].Done = a.Apply
		}
		v = fixes
	case "new":
		// This bridge reports a start without executing an agent. Its list
		// remains explicit, so tests can model a delayed or concurrent list.
		var a api.NewArgs
		_ = json.Unmarshal(req.Args, &a)
		if newID == "" {
			newID = fmt.Sprintf("fake%d", started)
		}
		v = api.SessionInfo{ID: newID, Name: a.Name, Agent: a.Agent, State: api.StateStarting, Alive: true}
	case "resume":
		var a api.ResumeArgs
		_ = json.Unmarshal(req.Args, &a)
		found := -1
		for i := range sessions {
			if sessions[i].ID == a.ID {
				found = i
				break
			}
		}
		if found < 0 {
			return proto.Response{ID: req.ID, Error: &proto.Error{Code: "session_not_found", Message: "no session has that ID"}}
		}
		s := sessions[found]
		if !client.Ended(s) {
			return proto.Response{ID: req.ID, Error: &proto.Error{Code: "agent_running", Message: "the agent is already running"}}
		}
		s.State, s.Alive = api.StateStarting, true
		s.ExitCode, s.Signal, s.Reason, s.OOM = nil, "", "", false
		if a.MemoryMax != 0 {
			s.MemoryMax = a.MemoryMax
		}
		t.mu.Lock()
		for i := range t.Sessions {
			if t.Sessions[i].ID == a.ID {
				t.Sessions[i] = s
				break
			}
		}
		t.mu.Unlock()
		v = api.ResumeResult{Session: s, Why: "started the agent again"}
	default:
		return proto.Response{ID: req.ID, Error: &proto.Error{Code: "unknown_op", Message: "unknown request " + req.Op}}
	}
	b, _ := json.Marshal(v)
	return proto.Response{ID: req.ID, OK: true, Result: b}
}

// has reports whether the helper that remote names is on the host.
func (t *Transport) has(remote string) bool {
	return t.Helper == "" || strings.Contains(remote, "/bin/"+t.Helper+"/")
}

// missingProc is a bridge whose helper is not on the host: the shell says
// so, and it ends at once.
type missingProc struct{}

func (missingProc) Stdin() io.WriteCloser { return nopCloser{} }
func (missingProc) Stdout() io.Reader     { return strings.NewReader("") }
func (missingProc) Stderr() string        { return "sh: 1: helper: No such file or directory" }
func (missingProc) Wait() error           { return errors.New("exit status 127") }
func (missingProc) Kill()                 {}

type nopCloser struct{}

func (nopCloser) Write(b []byte) (int, error) { return len(b), nil }
func (nopCloser) Close() error                { return nil }

type pipeProc struct {
	in     *io.PipeWriter
	out    *io.PipeReader
	done   chan struct{}
	stderr string
}

func (p *pipeProc) Stdin() io.WriteCloser { return p.in }
func (p *pipeProc) Stdout() io.Reader     { return p.out }
func (p *pipeProc) Stderr() string        { return p.stderr }
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
	// DestroyErr, if set, makes Destroy fail with it, and keep the
	// machine.
	DestroyErr error
	// StatusErr, if set, makes Status fail with it, as a provider that
	// cannot tell the machine's state does.
	StatusErr error
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

// Status reports the state, or fails with StatusErr.
func (m *Machine) Status(context.Context) (string, error) {
	m.call("status")
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.StatusErr != nil {
		return "", m.StatusErr
	}
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

// Destroy makes it missing, or fails with DestroyErr.
func (m *Machine) Destroy(context.Context) error {
	m.call("destroy")
	if m.DestroyErr != nil {
		return m.DestroyErr
	}
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
