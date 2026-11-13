package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// The golden screens are the same on every platform.
func init() { optKey = "alt" }

// now is the fixed clock of every test.
var now = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// fakeHost is the state of one pretend host, shared by its connections.
type fakeHost struct {
	sessions []client.Session
	info     client.HostInfo
	screens  map[string][]byte
	latency  time.Duration
	rtts     []time.Duration // when set, the round trips RoundTrip gives in turn
	pings    int             // calls to RoundTrip
	dialErr  error
	newErr   error
	// setupRepo is a repository with a setup script: a start there asks,
	// unless the user refused it, or trusted setupHash, the setup hash of
	// its scripts. An empty setupHash is an older helper, which sends no
	// hash.
	setupRepo, setupHash string
	// setupErr is the code of a start whose script cannot run, such as
	// setup_outside: every start fails with it, unless it skips the scripts.
	setupErr string
	// missing is a folder that does not exist: a start there asks, unless
	// it says to make the folder.
	missing string
	// checks are the host's agent checks (guide_fake_test.go).
	checks []client.AgentCheck
	// resumeErr is what a resume says, if it fails.
	resumeErr error
	// debugErr is what a debug start says, if it fails (debug_test.go).
	debugErr error
	// folders are more folders that the host suggests (clean_test.go).
	folders []string
}

// fakeBackend pretends to be SSH, the hosts and the terminal.
type fakeBackend struct {
	mu        sync.Mutex
	hosts     []client.Host
	state     map[string]*fakeHost
	conns     []*fakeConn
	dials     map[string]int
	calls     []string
	saved     [][]client.Host
	known     []string
	probe     client.Platform
	probeErr  error
	installed bool
	attach    client.AttachResult
	attachErr error
	attached  []client.AttachOptions
	blocked   *atomic.Int64 // goroutines waiting in Done
	mem       memory        // what the launcher remembers between runs
	saves     int
	secrets   map[string]api.Secret // a host's own secrets, for every start
	secretErr error
	trust     map[string]client.SetupAnswers // answers about setup scripts, by host
	trustWarn *client.UserError              // the answers' file did not parse
	// rememberWarn is the warning of the next answer: the file did not
	// parse when the answer was kept.
	rememberWarn *client.UserError
	local        *fakeLocal // the guided setup's computer (guide_fake_test.go)
	// attachHook runs during each attach, with the lock held: what the
	// user does in the session.
	attachHook       func(client.AttachOptions)
	dialHook         func(context.Context, client.Host)
	doctorHook       func(context.Context, string)
	rememberLastHook func(host, id, name string)
	// deleted, when set, is what a delete says, for any session.
	deleted *client.DeleteResult
	// debugFile is the launcher's own debug file while its debug is on
	// (debug_test.go).
	debugFile string
	// bundleGate, when set, holds a bundle until it closes.
	bundleGate chan struct{}
	// loadGate, when set, holds LoadHosts until it closes, and loading
	// closes when LoadHosts waits there. loadCtx is the context that the
	// last LoadHosts got. saveGate, saving and saveCtx do the same for
	// SaveHosts.
	loadGate, loading chan struct{}
	loadCtx           context.Context
	saveGate, saving  chan struct{}
	saveCtx           context.Context
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{state: map[string]*fakeHost{}, dials: map[string]int{}, blocked: &atomic.Int64{}}
}

func (b *fakeBackend) record(format string, a ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, fmt.Sprintf(format, a...))
}

func (b *fakeBackend) callList() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.calls...)
}

func (b *fakeBackend) LoadHosts(ctx context.Context) ([]client.Host, error) {
	b.mu.Lock()
	b.loadCtx = ctx
	b.mu.Unlock()
	if b.loadGate != nil {
		close(b.loading)
		<-b.loadGate
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]client.Host(nil), b.hosts...), nil
}

// SaveHosts saves as the real one does: nothing once ctx has ended.
func (b *fakeBackend) SaveHosts(ctx context.Context, h []client.Host) error {
	b.mu.Lock()
	b.saveCtx = ctx
	b.mu.Unlock()
	if b.saveGate != nil {
		close(b.saving)
		<-b.saveGate
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hosts = append([]client.Host(nil), h...)
	b.saved = append(b.saved, b.hosts)
	return nil
}

func (b *fakeBackend) Settings() client.Settings {
	var s client.Settings
	s.UI.Motion = "off"
	s.Attach.DetachKey = "ctrl+\\"
	return s
}

func (b *fakeBackend) HelperVersion() string { return "0.1.0-test" }

func (b *fakeBackend) Dial(ctx context.Context, h client.Host) (Conn, error) {
	b.mu.Lock()
	hook := b.dialHook
	b.mu.Unlock()
	if hook != nil {
		hook(ctx, h)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.dials[h.Name]++
	st := b.state[h.Name]
	if st == nil {
		st = &fakeHost{}
		b.state[h.Name] = st
	}
	if st.dialErr != nil {
		return nil, st.dialErr
	}
	c := &fakeConn{be: b, name: h.Name, done: make(chan struct{})}
	b.conns = append(b.conns, c)
	return c, nil
}

// conn is the newest connection to a host.
func (b *fakeBackend) conn(name string) *fakeConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.conns) - 1; i >= 0; i-- {
		if b.conns[i].name == name {
			return b.conns[i]
		}
	}
	return nil
}

func (b *fakeBackend) Probe(context.Context, client.Host) (client.Platform, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.probe
	p.Installed = b.installed
	return p, b.probeErr
}

func (b *fakeBackend) Install(_ context.Context, h client.Host, _ client.Platform) error {
	b.record("install %s", h.Name)
	b.mu.Lock()
	b.installed = true
	if b.state[h.Name] == nil {
		b.state[h.Name] = &fakeHost{info: client.HostInfo{Distro: "Ubuntu 24.04", Arch: "arm64", CPUs: 2, MemTotal: 4 << 30,
			MemoryMax: 3116367872, Agents: map[string]string{"claude": "/usr/bin/claude", "shell": "/bin/bash"}}}
	}
	b.mu.Unlock()
	return nil
}

func (b *fakeBackend) HelperSize(p client.Platform) (int, bool) {
	return 9_400_000, p.OS == "linux"
}

func (b *fakeBackend) SSHConfigHosts() []string { return b.known }

func (b *fakeBackend) Memory() memory {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mem
}

// Remember writes the sessions, as the real one does: never the last
// session.
func (b *fakeBackend) Remember(mem memory) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mem.Sessions = mem.Sessions
	b.saves++
}

func (b *fakeBackend) RememberLast(host, id, name string) {
	b.mu.Lock()
	b.mem.Last = &lastSession{Host: host, ID: id, Name: name}
	hook := b.rememberLastHook
	b.mu.Unlock()
	if hook != nil {
		hook(host, id, name)
	}
}

func (b *fakeBackend) Attach(_ context.Context, o client.AttachOptions) (client.AttachResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.attached = append(b.attached, o)
	if b.attachHook != nil {
		b.attachHook(o)
	}
	o.State.Key, o.State.Offset, o.State.Valid = o.Host.Name+"/"+o.Session, int64(100*len(b.attached)), true
	return b.attach, b.attachErr
}

// fakeConn is one pretend bridge connection.
type fakeConn struct {
	be     *fakeBackend
	name   string
	done   chan struct{}
	mu     sync.Mutex
	waits  int64
	closed bool
}

func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *fakeConn) host() *fakeHost {
	return c.be.state[c.name]
}

func (c *fakeConn) Folders(_ context.Context, prefix string) (client.FoldersResult, error) {
	all := []string{"~/src/api/", "~/src/app/", "~/notes/"}
	c.be.mu.Lock()
	all = append(all, c.host().folders...)
	c.be.mu.Unlock()
	r := client.FoldersResult{Recent: []string{"~/src/app"}, Repos: []string{"~/src/api", "~/src/app"}}
	for _, p := range all {
		if prefix != "" && strings.HasPrefix(p, prefix) {
			r.Matches = append(r.Matches, p)
		}
	}
	return r, nil
}

func (c *fakeConn) Sessions(context.Context) ([]client.Session, error) {
	if c.isClosed() {
		return nil, client.ErrClosed
	}
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	return append([]client.Session(nil), c.host().sessions...), nil
}

func (c *fakeConn) NewSession(_ context.Context, a client.NewArgs) (*client.Session, error) {
	for k := range a.Secrets {
		c.be.record("new %s secret %s=%v", c.name, k, a.Secrets[k])
	}
	if len(a.Argv) > 0 {
		c.be.record("new %s agent=%s argv=%v cwd=%s name=%s worktree=%v size=%dx%d", c.name, a.Agent, a.Argv, a.Cwd, a.Name, a.Worktree, a.Cols, a.Rows)
	} else {
		c.be.record("new %s agent=%s cwd=%s name=%s worktree=%v size=%dx%d", c.name, a.Agent, a.Cwd, a.Name, a.Worktree, a.Cols, a.Rows)
	}
	if a.Setup != "" {
		c.be.record("new %s setup=%s", c.name, a.Setup)
	}
	if len(a.SetupHashes) > 0 {
		c.be.record("new %s setup_hashes=%v", c.name, a.SetupHashes)
	}
	if a.MakeFolder {
		c.be.record("new %s mkdir %s", c.name, a.Cwd)
	}
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	h := c.host()
	if h.newErr != nil {
		return nil, h.newErr
	}
	if h.missing != "" && a.Cwd == h.missing && !a.MakeFolder {
		data, _ := json.Marshal(client.FolderQuestion{Folder: "/home/dev/" + strings.TrimPrefix(h.missing, "~/")})
		return nil, &client.UserError{Msg: "the folder does not exist", Code: api.CodeNoFolder, Data: data}
	}
	// The order of the host: the override, a refusal, the trusted hash.
	run, known := a.SetupTrust[h.setupRepo]
	refused := known && !run
	if h.setupRepo != "" && a.Setup == "" && !refused && (h.setupHash == "" || a.SetupHashes[h.setupRepo] != h.setupHash) {
		data, _ := json.Marshal(client.SetupQuestion{Repo: h.setupRepo, Script: h.setupRepo + "/.setup", Hash: h.setupHash})
		return nil, &client.UserError{Msg: "the repository has a setup script", Code: api.CodeSetupTrust, Data: data}
	}
	if h.setupErr != "" && a.Setup != api.SetupSkip {
		return nil, &client.UserError{Msg: "the setup script of /home/dev/src/app, .x/setup, points outside the repository, so it cannot run",
			Hint: "Make .x/setup a file in the repository, or start with --no-setup-script.", Code: h.setupErr}
	}
	name := a.Name
	if name == "" {
		name = "app"
	}
	s := client.Session{ID: fmt.Sprintf("n%03d", len(h.sessions)+1), Name: name, Agent: a.Agent, Cwd: a.Cwd,
		State: api.StateStarting, Since: now, CreatedAt: now, Alive: true}
	h.sessions = append(h.sessions, s)
	return &s, nil
}

// Resume starts a session's agent again: it works once more, with nothing
// of how it ended.
func (c *fakeConn) Resume(_ context.Context, a client.ResumeArgs) (*client.ResumeResult, error) {
	c.be.record("resume %s %s memory=%d secrets=%v", c.name, a.ID, a.MemoryMax, a.Secrets)
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	h := c.host()
	if h.resumeErr != nil {
		return nil, h.resumeErr
	}
	for i, s := range h.sessions {
		if s.ID == a.ID {
			s.State, s.Source, s.Since, s.Alive = api.StateWorking, "start", now, true
			s.Reason, s.Signal, s.ExitCode, s.OOM = "", "", nil, false
			h.sessions[i] = s
			return &client.ResumeResult{Session: s, Why: "resumed its own session"}, nil
		}
	}
	return nil, &client.UserError{Msg: "no session " + a.ID, Hint: "List sessions to find its ID."}
}

func (c *fakeConn) Delete(_ context.Context, id string) (client.DeleteResult, error) {
	c.be.record("delete %s %s", c.name, id)
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	h := c.host()
	for i, s := range h.sessions {
		if s.ID == id {
			h.sessions = append(h.sessions[:i], h.sessions[i+1:]...)
			res := client.DeleteResult{Deleted: true}
			if s.Name == "api-tests" {
				res.Worktree = "kept the worktree ~/src/api-tests: it has uncommitted changes"
			}
			if c.be.deleted != nil {
				res = *c.be.deleted
			}
			return res, nil
		}
	}
	return client.DeleteResult{}, &client.UserError{Msg: "no session " + id, Hint: "Refresh the list."}
}

func (c *fakeConn) RenameSession(_ context.Context, id, name string) error {
	c.be.record("rename %s %s %s", c.name, id, name)
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	for i := range c.host().sessions {
		if c.host().sessions[i].ID == id {
			c.host().sessions[i].Name = name
		}
	}
	return nil
}

func (c *fakeConn) Doctor(ctx context.Context) (client.HostInfo, error) {
	c.be.mu.Lock()
	hook := c.be.doctorHook
	c.be.mu.Unlock()
	if hook != nil {
		hook(ctx, c.name)
	}
	if err := ctx.Err(); err != nil {
		return client.HostInfo{}, err
	}
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	return c.host().info, nil
}

func (c *fakeConn) Peek(_ context.Context, id string, _ int) ([]byte, int, int, error) {
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	b, ok := c.host().screens[id]
	if !ok {
		return nil, 0, 0, errors.New("no screen kept")
	}
	return b, 100, 30, nil
}

func (c *fakeConn) Setup(context.Context) (client.SetupResult, error) {
	return client.SetupResult{Steps: []api.Check{
		{Name: "linger", OK: true, Detail: "on"},
		{Name: "terminfo", OK: true, Detail: "xterm-ghostty installed"},
	}}, nil
}

func (c *fakeConn) RoundTrip(context.Context) (time.Duration, error) {
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	h := c.host()
	h.pings++
	if len(h.rtts) > 0 {
		return h.rtts[(h.pings-1)%len(h.rtts)], nil
	}
	return h.latency, nil
}

// Done counts its callers as blocked, so the harness knows they wait.
func (c *fakeConn) Done() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.waits++
		c.be.blocked.Add(1)
	}
	return c.done
}

func (c *fakeConn) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.be.blocked.Add(-c.waits)
		close(c.done)
	}
}
