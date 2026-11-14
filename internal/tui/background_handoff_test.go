package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/bundle"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/diag"
	"github.com/iamlakshya/simstim/internal/onboard"
)

type handoffContextKey struct{}

type handoffCall struct {
	method  string
	calls   atomic.Int32
	entered chan context.Context
	release chan struct{}
}

func (c *handoffCall) run(ctx context.Context) error {
	c.calls.Add(1)
	if c.entered != nil {
		c.entered <- ctx
		<-ctx.Done()
		<-c.release
	}
	return ctx.Err()
}

type handoffBackend struct {
	*fakeBackend
	call *handoffCall
}

func (b *handoffBackend) gate(ctx context.Context, method string) error {
	if b.call != nil && b.call.method == method {
		return b.call.run(ctx)
	}
	return nil
}

func (b *handoffBackend) LoadHosts(ctx context.Context) ([]client.Host, error) {
	if err := b.gate(ctx, "save-load"); err != nil {
		return nil, err
	}
	return b.fakeBackend.LoadHosts(ctx)
}

func (b *handoffBackend) UpdateHostsContext(ctx context.Context, c client.HostChange) (warn, err error) {
	if _, err := b.LoadHosts(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.gate(ctx, "save-write"); err != nil {
		return nil, err
	}
	return b.fakeBackend.UpdateHostsContext(ctx, c)
}

func (b *handoffBackend) Probe(ctx context.Context, h client.Host) (client.Platform, error) {
	if err := b.gate(ctx, "probe"); err != nil {
		return client.Platform{}, err
	}
	return b.fakeBackend.Probe(ctx, h)
}

func (b *handoffBackend) Install(ctx context.Context, h client.Host, p client.Platform) error {
	if err := b.gate(ctx, "install"); err != nil {
		return err
	}
	return b.fakeBackend.Install(ctx, h, p)
}

func (b *handoffBackend) Dial(ctx context.Context, h client.Host) (Conn, error) {
	if err := b.gate(ctx, "dial"); err != nil {
		return nil, err
	}
	c, err := b.fakeBackend.Dial(ctx, h)
	if err != nil {
		return nil, err
	}
	return &handoffConn{fakeConn: c.(*fakeConn), call: b.call}, nil
}

func (b *handoffBackend) SaveFlight() (string, error) {
	if err := b.gate(context.Background(), "save-flight"); err != nil {
		return "", err
	}
	return b.fakeBackend.SaveFlight()
}

func (b *handoffBackend) Bundle(ctx context.Context, h client.Host, session string) (*bundle.Result, error) {
	if err := b.gate(ctx, "bundle"); err != nil {
		return nil, err
	}
	return b.fakeBackend.Bundle(ctx, h, session)
}

func (b *handoffBackend) SSHConfigHosts() []string {
	_ = b.gate(context.Background(), "ssh-hosts")
	return b.fakeBackend.SSHConfigHosts()
}

func (b *handoffBackend) Local() onboard.Local {
	return &handoffLocal{Local: b.fakeBackend.Local(), call: b.call}
}

type handoffLocal struct {
	onboard.Local
	call *handoffCall
}

func (l *handoffLocal) gate(ctx context.Context, method string) error {
	if l.call != nil && l.call.method == method {
		return l.call.run(ctx)
	}
	return nil
}

func (l *handoffLocal) Places() []onboard.Place {
	_ = l.gate(context.Background(), "local-places")
	return l.Local.Places()
}

func (l *handoffLocal) PublicKey() string {
	_ = l.gate(context.Background(), "local-key")
	return l.Local.PublicKey()
}

func (l *handoffLocal) HasSSHHost(name string) bool {
	_ = l.gate(context.Background(), "local-name")
	return l.Local.HasSSHHost(name)
}

func (l *handoffLocal) SSHWorks(ctx context.Context, target string) error {
	if err := l.gate(ctx, "local-ssh"); err != nil {
		return err
	}
	return l.Local.SSHWorks(ctx, target)
}

type handoffConn struct {
	*fakeConn
	call *handoffCall
}

func (c *handoffConn) gate(ctx context.Context, method string) error {
	if c.call != nil && c.call.method == method {
		return c.call.run(ctx)
	}
	return nil
}

func (c *handoffConn) NewSession(ctx context.Context, a client.NewArgs) (*client.Session, error) {
	if err := c.gate(ctx, "new"); err != nil {
		return nil, err
	}
	return c.fakeConn.NewSession(ctx, a)
}

func (c *handoffConn) Resume(ctx context.Context, a client.ResumeArgs) (*client.ResumeResult, error) {
	if err := c.gate(ctx, "resume"); err != nil {
		return nil, err
	}
	return c.fakeConn.Resume(ctx, a)
}

func (c *handoffConn) DeleteNamed(ctx context.Context, id, name string) (client.DeleteResult, error) {
	if err := c.gate(ctx, "delete"); err != nil {
		return client.DeleteResult{}, err
	}
	return c.fakeConn.DeleteNamed(ctx, id, name)
}

func (c *handoffConn) RoundTrip(ctx context.Context) (time.Duration, error) {
	if err := c.gate(ctx, "ping"); err != nil {
		return 0, err
	}
	return c.fakeConn.RoundTrip(ctx)
}

func (c *handoffConn) Folders(ctx context.Context, p string) (client.FoldersResult, error) {
	if err := c.gate(ctx, "folders"); err != nil {
		return client.FoldersResult{}, err
	}
	return c.fakeConn.Folders(ctx, p)
}

func (c *handoffConn) Setup(ctx context.Context) (client.SetupResult, error) {
	if err := c.gate(ctx, "setup"); err != nil {
		return client.SetupResult{}, err
	}
	return c.fakeConn.Setup(ctx)
}

func (c *handoffConn) AgentChecks(ctx context.Context) ([]client.AgentCheck, error) {
	if err := c.gate(ctx, "agents"); err != nil {
		return nil, err
	}
	return c.fakeConn.AgentChecks(ctx)
}

func (c *handoffConn) DebugStart(ctx context.Context, id string, cats diag.Cats, d time.Duration) (client.DebugState, error) {
	if err := c.gate(ctx, "debug-start"); err != nil {
		return client.DebugState{}, err
	}
	return c.fakeConn.DebugStart(ctx, id, cats, d)
}

func (c *handoffConn) StartConnDebug(ctx context.Context, cats diag.Cats) (client.DebugState, error) {
	if err := c.gate(ctx, "debug-start-host"); err != nil {
		return client.DebugState{}, err
	}
	return c.fakeConn.StartConnDebug(ctx, cats)
}

func (c *handoffConn) StopConnDebug(ctx context.Context) error {
	if err := c.gate(ctx, "debug-stop"); err != nil {
		return err
	}
	return c.fakeConn.StopConnDebug(ctx)
}

func (c *handoffConn) DebugSave(ctx context.Context, id string) (client.DebugFileInfo, error) {
	if err := c.gate(ctx, "debug-save"); err != nil {
		return client.DebugFileInfo{}, err
	}
	return c.fakeConn.DebugSave(ctx, id)
}

type handoffCommandCase struct {
	name, method string
	start        func(*Model, *hostConn, *wizard) tea.Cmd
}

func handoffCommandCases() []handoffCommandCase {
	return []handoffCommandCase{
		{"operation", "op", func(m *Model, h *hostConn, _ *wizard) tea.Cmd {
			return m.op(h, "done", func(ctx context.Context, c Conn) error {
				return unclean(c).(*handoffConn).call.run(ctx)
			})
		}},
		{"new session", "new", func(m *Model, h *hostConn, _ *wizard) tea.Cmd {
			return m.startSession(h, startReq{args: client.NewArgs{Agent: "shell"}})
		}},
		{"delete session", "delete", func(m *Model, h *hostConn, _ *wizard) tea.Cmd {
			return m.deleteSession(h.name(), h.sessions[0])
		}},
		{"load saved hosts", "save-load", func(m *Model, h *hostConn, _ *wizard) tea.Cmd {
			return m.saveHosts(h.host, false)
		}},
		{"write saved hosts", "save-write", func(m *Model, h *hostConn, _ *wizard) tea.Cmd {
			return m.saveHosts(h.host, false)
		}},
		{"test host", "ping", func(m *Model, h *hostConn, _ *wizard) tea.Cmd { return m.testHost(h) }},
		{"quick folders", "folders", func(m *Model, h *hostConn, _ *wizard) tea.Cmd {
			m.prompt = &promptState{host: h.name(), in: newInput("~/src", "")}
			return m.askFolders()
		}},
		{"form folders", "folders", func(m *Model, h *hostConn, _ *wizard) tea.Cmd {
			return m.fetchFolders(&newSession{host: h.name(), folder: newInput("~/src", "")})
		}},
		{"resume", "resume", func(m *Model, _ *hostConn, _ *wizard) tea.Cmd { return m.resumeSelected() }},
		{"debug start", "debug-start", func(m *Model, _ *hostConn, _ *wizard) tea.Cmd { return m.debugStart() }},
		{"debug start host", "debug-start-host", func(m *Model, _ *hostConn, _ *wizard) tea.Cmd {
			m.screen = scrHosts
			return m.debugStart()
		}},
		{"debug stop", "debug-stop", func(m *Model, h *hostConn, _ *wizard) tea.Cmd {
			return m.debugOp(h.name(), func(ctx context.Context, c debugConn) error { return c.StopConnDebug(ctx) })
		}},
		{"debug save", "debug-save", func(m *Model, _ *hostConn, _ *wizard) tea.Cmd { return m.debugSave() }},
		{"debug bundle", "bundle", func(m *Model, _ *hostConn, _ *wizard) tea.Cmd { return m.debugBundle() }},
		{"wizard probe", "probe", func(m *Model, _ *hostConn, w *wizard) tea.Cmd { return w.probe(m) }},
		{"wizard install", "install", func(m *Model, _ *hostConn, w *wizard) tea.Cmd { return w.install(m) }},
		{"wizard dial", "dial", func(m *Model, _ *hostConn, w *wizard) tea.Cmd { return w.setup(m) }},
		{"wizard setup", "setup", func(m *Model, _ *hostConn, w *wizard) tea.Cmd { return w.setup(m) }},
		{"wizard agents", "agents", func(m *Model, _ *hostConn, w *wizard) tea.Cmd {
			w.saved, w.step = true, wzReady
			return w.loadAgents(m)
		}},
		{"wizard action", "new", func(m *Model, _ *hostConn, w *wizard) tea.Cmd { w.saved, w.step = true, wzRun; return w.runAct(m) }},
		{"guided SSH check", "local-ssh", func(m *Model, _ *hostConn, _ *wizard) tea.Cmd {
			g := &guide{step: gdRentConfig}
			return g.sshCheck(m, "box")
		}},
	}
}

func newHandoffCommandModel(t *testing.T) (*Model, *hostConn, *wizard, *handoffBackend, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), handoffContextKey{}, "kept"))
	be := &handoffBackend{fakeBackend: newFakeBackend()}
	be.hosts = []client.Host{{Name: "box", SSH: "box"}}
	s := client.Session{ID: "s1", Name: "job", Agent: "shell", State: "idle", Alive: true}
	be.state["box"] = &fakeHost{sessions: []client.Session{s}}
	m := New(ctx, be, Options{Now: func() time.Time { return now }})
	m.after = func(time.Duration, tea.Msg) tea.Cmd { return nil }
	h := m.hostByName("box")
	c, err := be.Dial(ctx, h.host)
	if err != nil {
		t.Fatal(err)
	}
	h.conn, h.status, h.sessions, h.loaded = c, stOnline, []client.Session{s}, true
	m.conns.add(c)
	m.screen, m.cur, m.sel, m.autoSel = scrSessions, "box", sessKey{"box", "s1"}, false
	m.syncSel()
	w := newWizard()
	w.host = h.host
	w.act.Args = client.NewArgs{Agent: "shell"}
	m.modal = w
	t.Cleanup(func() { cancel(); m.Close() })
	return m, h, w, be, cancel
}

func executeHandoffCommand(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		var wg sync.WaitGroup
		for _, next := range batch {
			wg.Go(func() { executeHandoffCommand(next) })
		}
		wg.Wait()
	}
}

func TestBackgroundRPCsCancelAndJoinAtAttachHandoff(t *testing.T) {
	for _, tc := range handoffCommandCases() {
		t.Run(tc.name, func(t *testing.T) {
			m, h, w, be, cancel := newHandoffCommandModel(t)
			call := &handoffCall{method: tc.method, entered: make(chan context.Context, 1), release: make(chan struct{})}
			be.call, unclean(h.conn).(*handoffConn).call = call, call
			var release sync.Once
			letGo := func() { release.Do(func() { close(call.release) }) }
			t.Cleanup(func() { cancel(); letGo() })
			cmd := tc.start(m, h, w)
			ps := newProgStepper(t, m, m)
			var ctx context.Context
			finished := ps.stepToGate(cmd, func() bool {
				select {
				case ctx = <-call.entered:
					return true
				default:
					return false
				}
			})
			if ctx.Value(handoffContextKey{}) != "kept" {
				t.Fatal("the request lost its parent context values")
			}
			paused := make(chan struct{})
			go func() { m.pauseBg(); close(paused) }()
			select {
			case <-ctx.Done():
			case <-paused:
				t.Fatal("the launcher handed off while its request was still live")
			case <-time.After(10 * time.Second):
				t.Fatal("the request did not stop at handoff")
			}
			letGo()
			select {
			case <-paused:
			case <-time.After(10 * time.Second):
				t.Fatal("the launcher did not join the stopped request")
			}
			<-finished
			if ctx.Err() != context.Canceled || call.calls.Load() != 1 {
				t.Fatalf("request: err=%v calls=%d", ctx.Err(), call.calls.Load())
			}
		})
	}
}

func TestDeferredBackgroundCommandsDoNotStartAfterAttachHandoff(t *testing.T) {
	cases := handoffCommandCases()
	cases = append(cases,
		handoffCommandCase{"save local flight", "save-flight", func(m *Model, _ *hostConn, _ *wizard) tea.Cmd {
			m.be.(*handoffBackend).debugFile = "debug"
			return m.debugSave()
		}},
		handoffCommandCase{"load SSH aliases", "ssh-hosts", func(m *Model, _ *hostConn, _ *wizard) tea.Cmd { return m.openWizard() }},
		handoffCommandCase{"guided places", "local-places", func(m *Model, _ *hostConn, _ *wizard) tea.Cmd { return (&guide{}).probe(m, "") }},
		handoffCommandCase{"guided public key", "local-key", func(m *Model, _ *hostConn, _ *wizard) tea.Cmd { return (&guide{step: gdRentKey}).findKey(m) }},
		handoffCommandCase{"guided SSH name", "local-name", func(m *Model, _ *hostConn, _ *wizard) tea.Cmd {
			g := &guide{step: gdRentName, in: newInput("fresh", "")}
			return g.nameServer(m)
		}},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, h, w, be, _ := newHandoffCommandModel(t)
			call := &handoffCall{method: tc.method}
			be.call, unclean(h.conn).(*handoffConn).call = call, call
			cmd := tc.start(m, h, w)
			m.pauseBg()
			executeHandoffCommand(cmd)
			if n := call.calls.Load(); n != 0 {
				t.Fatalf("request started %d times after the launcher handed off", n)
			}
		})
	}
}
