package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"

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
	dialErr  error
	newErr   error
	// setupRepo is a repository with a setup script: a start there asks,
	// unless it is answered.
	setupRepo string
	// missing is a folder that does not exist: a start there asks, unless
	// it says to make the folder.
	missing string
	// checks are the host's agent checks (guide_fake_test.go).
	checks []client.AgentCheck
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
	trust     map[string]map[string]bool // answers about setup scripts, by host and repository
	local     *fakeLocal                 // the guided setup's computer (guide_fake_test.go)
	// attachHook runs during each attach, with the lock held: what the
	// user does in the session.
	attachHook func(client.AttachOptions)
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

func (b *fakeBackend) LoadHosts() ([]client.Host, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]client.Host(nil), b.hosts...), nil
}

func (b *fakeBackend) SaveHosts(h []client.Host) error {
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

func (b *fakeBackend) Dial(_ context.Context, h client.Host) (Conn, error) {
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
			MemoryMax: 1610612736, Agents: map[string]string{"claude": "/usr/bin/claude", "shell": "/bin/bash"}}}
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

func (b *fakeBackend) Remember(mem memory) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mem = mem
	b.saves++
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
	if _, known := a.SetupTrust[h.setupRepo]; h.setupRepo != "" && a.Setup == "" && !known {
		data, _ := json.Marshal(client.SetupQuestion{Repo: h.setupRepo, Script: h.setupRepo + "/.setup"})
		return nil, &client.UserError{Msg: "the repository has a setup script", Code: api.CodeSetupTrust, Data: data}
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

func (c *fakeConn) Doctor(context.Context) (client.HostInfo, error) {
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
	return c.host().latency, nil
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

// Harness: runs the model the way Bubble Tea would, with fake time.

type timerMsg struct {
	d   time.Duration
	msg tea.Msg
}

type harness struct {
	t       *testing.T
	m       *Model
	be      *fakeBackend
	clock   time.Time
	results chan tea.Msg
	running atomic.Int64
	timers  []timerMsg
	quit    bool
	raw     []string // what the model wrote to the terminal itself
}

func newHarness(t *testing.T, be *fakeBackend, w, h int) *harness {
	t.Helper()
	hs := &harness{t: t, be: be, clock: now, results: make(chan tea.Msg, 4096)}
	m := New(context.Background(), be, Options{Now: func() time.Time { return hs.clock }})
	m.after = func(d time.Duration, msg tea.Msg) tea.Cmd {
		return func() tea.Msg { return timerMsg{d, msg} }
	}
	m.exec = func(c tea.ExecCommand, fn tea.ExecCallback) tea.Cmd {
		return func() tea.Msg { return fn(c.Run()) }
	}
	hs.m = m
	t.Cleanup(m.Close)
	hs.send(tea.WindowSizeMsg{Width: w, Height: h})
	hs.do(m.Init())
	return hs
}

// do runs cmd and everything it leads to, until only goroutines waiting
// for a connection to end are left.
func (hs *harness) do(cmd tea.Cmd) {
	hs.t.Helper()
	hs.start(cmd)
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case msg := <-hs.results:
			hs.deliver(msg)
			continue
		default:
		}
		if hs.running.Load() <= hs.be.blocked.Load() {
			// Settle once more, in case a waiter just woke.
			time.Sleep(2 * time.Millisecond)
			if len(hs.results) == 0 && hs.running.Load() <= hs.be.blocked.Load() {
				return
			}
			continue
		}
		if time.Now().After(deadline) {
			hs.t.Fatalf("commands did not settle: %d running, %d waiting", hs.running.Load(), hs.be.blocked.Load())
		}
		time.Sleep(time.Millisecond)
	}
}

func (hs *harness) start(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	hs.running.Add(1)
	go func() {
		msg := cmd()
		hs.results <- msg
		hs.running.Add(-1)
	}()
}

func (hs *harness) deliver(msg tea.Msg) {
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			hs.start(c)
		}
	case timerMsg:
		hs.timers = append(hs.timers, msg)
	case tea.QuitMsg:
		hs.quit = true
	case tea.RawMsg:
		hs.raw = append(hs.raw, fmt.Sprint(msg.Msg))
	default:
		_, cmd := hs.m.Update(msg)
		hs.start(cmd)
	}
}

// send delivers one message and settles.
func (hs *harness) send(msg tea.Msg) {
	hs.t.Helper()
	_, cmd := hs.m.Update(msg)
	hs.do(cmd)
}

// key presses keys by name, in order.
func (hs *harness) key(keys ...string) {
	hs.t.Helper()
	for _, k := range keys {
		hs.send(keyMsg(k))
	}
}

// typeText types each rune as a key press.
func (hs *harness) typeText(s string) {
	hs.t.Helper()
	for _, r := range s {
		hs.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// fire delivers the pending timers whose message matches.
func (hs *harness) fire(match func(tea.Msg) bool) int {
	hs.t.Helper()
	var keep []timerMsg
	var due []timerMsg
	for _, t := range hs.timers {
		if match(t.msg) {
			due = append(due, t)
		} else {
			keep = append(keep, t)
		}
	}
	hs.timers = keep
	for _, t := range due {
		hs.send(t.msg)
	}
	return len(due)
}

func keyMsg(k string) tea.KeyPressMsg {
	special := map[string]tea.KeyPressMsg{
		"enter":      {Code: tea.KeyEnter},
		"ctrl+enter": {Code: tea.KeyEnter, Mod: tea.ModCtrl},
		"esc":        {Code: tea.KeyEscape},
		"tab":        {Code: tea.KeyTab},
		"shift+tab":  {Code: tea.KeyTab, Mod: tea.ModShift},
		"up":         {Code: tea.KeyUp},
		"down":       {Code: tea.KeyDown},
		"left":       {Code: tea.KeyLeft},
		"right":      {Code: tea.KeyRight},
		"backspace":  {Code: tea.KeyBackspace},
		"space":      {Code: tea.KeySpace, Text: " "},
		"ctrl+c":     {Code: 'c', Mod: tea.ModCtrl},
		"ctrl+o":     {Code: 'o', Mod: tea.ModCtrl},
		"ctrl+u":     {Code: 'u', Mod: tea.ModCtrl},
		"ctrl+f":     {Code: 'f', Mod: tea.ModCtrl},
		"alt+e":      {Code: 'e', Mod: tea.ModAlt},
	}
	if m, ok := special[k]; ok {
		return m
	}
	r := []rune(k)
	return tea.KeyPressMsg{Code: r[0], Text: k}
}

// Fixtures.

func ago(d time.Duration) time.Time { return now.Add(-d) }

func code(n int) *int { return &n }

// labBackend is three hosts: two online with sessions, one unreachable.
func labBackend() *fakeBackend {
	be := newFakeBackend()
	be.hosts = []client.Host{
		{Name: "atlas-dev", SSH: "atlas-dev"},
		{Name: "gcp-dev", SSH: "dev@203.0.113.7", SSHConfig: "~/src/infra/ssh/config"},
		{Name: "aws-gpu", SSH: "aws-gpu"},
	}
	be.state["atlas-dev"] = &fakeHost{
		latency: 9 * time.Millisecond,
		info: client.HostInfo{User: "dev", Distro: "Debian 13", Arch: "amd64", CPUs: 4, MemTotal: 8 << 30, DiskFree: 61 << 30,
			Version: "0.1.0", MemoryMax: 3435973837,
			Agents: map[string]string{"claude": "/usr/local/bin/claude", "codex": "/usr/local/bin/codex", "opencode": "/usr/bin/opencode", "shell": "/usr/bin/zsh"}},
		sessions: []client.Session{
			{ID: "s1", Name: "docs-pass", Agent: "opencode", Cwd: "/home/dev/src/docs", State: api.StateExited, ExitCode: code(0), Since: ago(3 * time.Hour), CreatedAt: ago(5 * time.Hour)},
			{ID: "s2", Name: "notes", Agent: "shell", Cwd: "/home/dev", State: api.StateIdle, Since: ago(time.Hour), CreatedAt: ago(26 * time.Hour), Alive: true},
			{ID: "s3", Name: "api-tests", Agent: "codex", Cwd: "/home/dev/src/api", State: api.StateWorking, Since: ago(14 * time.Second), CreatedAt: ago(40 * time.Minute), Alive: true},
			{ID: "s4", Name: "auth-refactor", Agent: "claude", Cwd: "/home/dev/src/app", State: api.StateNeedsInput, Since: ago(2 * time.Minute), CreatedAt: ago(time.Hour),
				Alive: true, MemoryCurrent: 6549825126, MemoryMax: 8 << 30},
		},
		screens: map[string][]byte{
			"s4": []byte("\x1b[?1049h\x1b[2J\x1b[H\x1b[c" +
				"\x1b[1m✻ Welcome back\x1b[0m\r\n\r\n" +
				"> Refactor the auth module to use the new token store\r\n\r\n" +
				"\x1b[32m●\x1b[0m I'll start by reading the current implementation.\r\n\r\n" +
				"  Read internal/auth/store.go (212 lines)\r\n" +
				"  Read internal/auth/session.go (96 lines)\r\n\r\n" +
				"\x1b[33m●\x1b[0m Should I also migrate the old sessions table? (y/n)\r\n\r\n" +
				"> "),
			"s3": []byte("running 48 tests\r\n\x1b[32mok\x1b[0m  api/handlers  0.41s\r\n"),
		},
	}
	be.state["gcp-dev"] = &fakeHost{
		latency: 41 * time.Millisecond,
		info:    client.HostInfo{Distro: "Ubuntu 24.04", Arch: "amd64", CPUs: 8, MemTotal: 32 << 30, MemoryMax: 8 << 30, Agents: map[string]string{"gemini": "/usr/bin/gemini", "shell": "/bin/bash"}},
		sessions: []client.Session{
			{ID: "g1", Name: "bench", Agent: "gemini", Cwd: "/srv/bench", State: api.StateWorking, Since: ago(5 * time.Minute), CreatedAt: ago(2 * time.Hour), Alive: true},
			{ID: "g2", Name: "scratch", Agent: "shell", Cwd: "/tmp", State: api.StateExited, ExitCode: code(1), Since: ago(50 * time.Hour), CreatedAt: ago(60 * time.Hour)},
		},
	}
	be.state["aws-gpu"] = &fakeHost{dialErr: &client.UserError{
		Msg:  "cannot reach aws-gpu: ssh: connect to host 198.51.100.4 port 22: Operation timed out",
		Hint: "ssh aws-gpu   (check that this works first)",
	}}
	return be
}

func uvDraw(c *lipgloss.Canvas, s string) { uv.NewStyledString(s).Draw(c, c.Bounds()) }
