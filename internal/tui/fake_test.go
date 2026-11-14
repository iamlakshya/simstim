package tui

import (
	"context"
	"fmt"
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
	grantErr error
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
	folders   []string
	hostID    string
	idChanged bool
}

// fakeBackend pretends to be SSH, the hosts and the terminal.
type fakeBackend struct {
	settingsWarn, detachKey string
	settings                *client.Settings
	newArgs                 []client.NewArgs
	secretsFrom             []client.Settings
	mu                      sync.Mutex
	hosts                   []client.Host
	tiers                   map[string]client.TierEntry
	tiersErr                error
	state                   map[string]*fakeHost
	conns                   []*fakeConn
	dials                   map[string]int
	calls                   []string
	saved                   [][]client.Host
	known                   []string
	probe                   client.Platform
	probeErr                error
	installed               bool
	attach                  client.AttachResult
	attachErr               error
	attached                []client.AttachOptions
	blocked                 *atomic.Int64 // goroutines waiting in Done
	mem                     memory        // what the launcher remembers between runs
	saves                   int
	secrets                 map[string]api.Secret // a host's own secrets, for every start
	secretErr               error
	trust                   map[string]client.SetupAnswers // answers about setup scripts, by host
	trustWarn               *client.UserError              // the answers' file did not parse
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
	// pinUnknown says that the user's settings do not name the fake host's
	// key, and pinErr is what a pin says, if it fails.
	pinUnknown bool
	pinErr     error
	staged     map[string]string // the pin staged for a host, "" for none yet
	// stageGate, when set, holds StagePin until it closes or ctx ends.
	stageGate chan struct{}
	// beforeUpdate and beforeInstall run at the start of UpdateHosts and
	// Install: what another command does at that moment. updateErr is what
	// UpdateHosts returns, such as host_busy while a command holds the lock.
	beforeUpdate  func(client.HostChange)
	beforeInstall func(client.Host)
	updateErr     error
	resolveTier   func(context.Context, client.Host) (client.Tier, error)
	retire        func(context.Context, client.Host) error
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

// UpdateHosts saves or forgets one host, with the compare of the real one
// (client.ApplyHostChange). The launcher has no other way to write the
// hosts. beforeUpdate, when set, runs first: another command that changes
// the hosts just before the save.
func (b *fakeBackend) UpdateHosts(c client.HostChange) (warn, err error) {
	return b.UpdateHostsContext(context.Background(), c)
}

func (b *fakeBackend) UpdateHostsContext(ctx context.Context, c client.HostChange) (warn, err error) {
	b.mu.Lock()
	b.saveCtx = ctx
	b.mu.Unlock()
	if b.saveGate != nil {
		if b.saving != nil {
			close(b.saving)
		}
		select {
		case <-b.saveGate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.beforeUpdate != nil {
		b.beforeUpdate(c)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.updateErr != nil {
		b.mu.Unlock()
		return nil, b.updateErr
	}
	out, err := client.ApplyHostChange(b.hosts, c)
	if err != nil {
		b.mu.Unlock()
		return nil, err
	}
	pin := ""
	if c.Now != nil {
		pin = b.staged[c.Now.Name]
	}
	if c.Pin && c.Now != nil {
		delete(b.staged, c.Now.Name)
	}
	b.hosts = out
	b.saved = append(b.saved, out)
	if b.tiers == nil {
		b.tiers = map[string]client.TierEntry{}
	}
	switch {
	case c.Now == nil && c.Was != nil:
		delete(b.tiers, c.Was.Name)
	case c.Now != nil:
		e := b.tiers[c.Now.Name]
		e.Name = c.Now.Name
		if c.Tier.Valid() {
			e.Tier, e.IDChanged = c.Tier, false
		}
		if c.HostID != "" {
			e.HostID = c.HostID
		}
		if pin != "" {
			e.Key = pin
		}
		if e.Tier.Valid() {
			b.tiers[c.Now.Name] = e
		}
	}
	b.mu.Unlock()
	if c.Now == nil {
		b.mu.Lock()
		delete(b.staged, c.Was.Name)
		b.mu.Unlock()
		b.record("forget %s", c.Was.Name)
		// The forget's clean-up of the record (errClearRemoval).
		b.record("clear removal %s", c.Was.Name)
		return errClearRemoval, nil
	}
	return nil, nil
}

func (b *fakeBackend) Settings() client.Settings {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.settings != nil {
		return *b.settings
	}
	var s client.Settings
	s.UI.Motion = "off"
	s.Attach.DetachKey = "ctrl+\\"
	if b.detachKey != "" {
		s.Attach.DetachKey = b.detachKey
	}
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
	id := st.hostID
	if id == "" {
		id = "0123456789abcdef0123456789abcdef"
	}
	changed := st.idChanged
	if changed {
		st.idChanged = false
		if b.tiers != nil {
			e := b.tiers[h.Name]
			e.Name, e.Tier, e.HostID, e.IDChanged = h.Name, client.TierShared, id, true
			b.tiers[h.Name] = e
		}
	}
	c := &fakeConn{be: b, name: h.Name, hostID: id, idChanged: changed, done: make(chan struct{})}
	b.conns = append(b.conns, c)
	return c, nil
}

func (b *fakeBackend) Reconnect(ctx context.Context, h client.Host) (Conn, error) {
	b.record("reconnect %s", h.Name)
	b.mu.Lock()
	retire := b.retire
	b.mu.Unlock()
	if retire != nil {
		if err := retire(ctx, h); err != nil {
			return nil, err
		}
	}
	return b.Dial(ctx, h)
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

func (b *fakeBackend) Probe(_ context.Context, h client.Host) (client.Platform, error) {
	b.record("probe %s", h.Name)
	b.mu.Lock()
	defer b.mu.Unlock()
	p := b.probe
	p.Installed = b.installed
	return p, b.probeErr
}

func (b *fakeBackend) Install(_ context.Context, h client.Host, _ client.Platform) error {
	b.record("install %s", h.Name)
	if b.beforeInstall != nil {
		b.beforeInstall(h)
	}
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

// Remember writes the lists, as the real one does: never the last
// session.
func (b *fakeBackend) Remember(lists map[string]client.HostList) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mem.Sessions = map[string][]client.Session{}
	b.mem.Seen = map[string]time.Time{}
	for h, l := range lists {
		b.mem.Sessions[h] = l.Sessions
		b.mem.Seen[h] = l.At
	}
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
func (b *fakeBackend) SaveHosts(ctx context.Context, hosts []client.Host) error {
	b.mu.Lock()
	b.saveCtx = ctx
	b.mu.Unlock()
	if b.saveGate != nil {
		if b.saving != nil {
			close(b.saving)
		}
		select {
		case <-b.saveGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hosts = append([]client.Host(nil), hosts...)
	b.saved = append(b.saved, append([]client.Host(nil), hosts...))
	return nil
}

func (b *fakeBackend) LoadTiers(ctx context.Context, hosts []client.Host) (client.Tiers, error) {
	if err := ctx.Err(); err != nil {
		return client.Tiers{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tiersErr != nil {
		return client.Tiers{}, b.tiersErr
	}
	if b.tiers == nil {
		b.tiers = make(map[string]client.TierEntry, len(hosts))
		for _, h := range hosts {
			b.tiers[h.Name] = client.TierEntry{Name: h.Name, Tier: client.TierOwn}
		}
	}
	return client.NewTiers(b.tiers), nil
}

func (b *fakeBackend) ResolveHostTier(ctx context.Context, h client.Host) (client.Tier, error) {
	if b.resolveTier != nil {
		return b.resolveTier(ctx, h)
	}
	ts, err := b.LoadTiers(ctx, []client.Host{h})
	if err == nil && ts.NeedsAnswer(h.Name) {
		return client.TierShared, &client.UserError{Msg: "the host tier needs an answer", Hint: "Choose a host tier.", Code: "needs_answer", Kind: client.KindNeedsAnswer}
	}
	return ts.Of(h.Name), err
}

func (c *fakeConn) HostID() string { return c.hostID }

func (c *fakeConn) HostIDChanged() bool { return c.idChanged }

func (b *fakeBackend) ReadSettings() (client.Settings, string) {
	s := b.Settings()
	b.mu.Lock()
	defer b.mu.Unlock()
	return s, b.settingsWarn
}
