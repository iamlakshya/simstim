package e2b

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

// CreateMachineV1 is the closed producer for a session's own E2B machine.
// The resource handle owns the API client that actually created its sandbox.
// A Host is only reporting and saved-entry data; it cannot recover this handle.
func (p Provider) CreateMachineV1(ctx context.Context, req provider.Request) (client.Host, client.SessionMachineResourceV1, error) {
	request := machineCreationRequest(req)
	return p.createOwnedMachine(ctx, req, func() error { return client.CheckSessionMachineCreationContextV1(ctx, request) })
}

func (p Provider) createOwnedMachine(ctx context.Context, req provider.Request, check func() error) (client.Host, client.SessionMachineResourceV1, error) {
	if err := check(); err != nil {
		return client.Host{}, nil, err
	}
	p = freezeMachineProvider(p)
	h, box, err := p.createWithCheck(ctx, req, check)
	if err != nil {
		return h, nil, err
	}
	resource := &createdMachine{host: cloneMachineHost(h), box: box, processes: make(map[*createdMachineProcess]struct{})}
	if !validMachineID(box.id) {
		// An unusable response is never turned into a request path.
		return h, resource, machineIdentityError()
	}
	// Only the original creation reply selects the envd endpoint. A later
	// status or resume reply may rotate its token, never its locator.
	box.p.SandboxURL = box.p.envdURL(*box.last)
	if !validMachineOrigin(box.p.APIURL) || !validMachineOrigin(box.p.SandboxURL) {
		return h, resource, machineIdentityError()
	}
	h.Machine.Endpoint, h.Machine.TransportURL = box.p.APIURL, box.p.SandboxURL
	resource.host = cloneMachineHost(h)
	if err := check(); err != nil {
		return h, resource, err
	}
	return h, resource, nil
}

func machineCreationRequest(req provider.Request) client.HostCreationRequest {
	return client.HostCreationRequest{Name: req.Name, Provider: "e2b", CPUs: req.CPUs, Memory: req.Memory, Disk: req.Disk,
		Image: req.Image, Template: req.Template, IdleStop: req.IdleStop, TokenCommand: req.TokenCommand}
}

func freezeMachineProvider(p Provider) Provider {
	p.APIURL = p.apiURL()
	if p.CLIConfig == "" {
		p.CLIConfig = cliConfigPath()
	}
	if p.SandboxURL == "" {
		p.SandboxURL = os.Getenv("E2B_SANDBOX_URL")
	}
	p.frozenDomain, p.frozen = domain(), true
	c := p.HTTP
	if c == nil {
		c = http.DefaultClient
	}
	copy := *c
	redirect := copy.CheckRedirect
	copy.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) == 0 || !sameMachineOrigin(req.URL, via[0].URL) {
			return errors.New("E2B refused a redirect outside the original provider endpoint")
		}
		if redirect != nil {
			return redirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("E2B stopped after ten redirects")
		}
		return nil
	}
	p.HTTP = &copy
	return p
}

func sameMachineOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && a.Scheme == b.Scheme && a.Host == b.Host
}

func validMachineOrigin(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == "" && (parsed.Path == "" || parsed.Path == "/")
}

func validMachineID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func cloneMachineHost(h client.Host) client.Host {
	if h.Machine != nil {
		machine := *h.Machine
		machine.Source = client.CloneMachineSource(machine.Source)
		h.Machine = &machine
	}
	return h
}

func machineIdentityError() error {
	return &client.UserError{Code: "machine_identity", Msg: "E2B returned another or an invalid machine identity",
		Hint: "Inspect the original machine in E2B before retrying this operation.", Kind: client.KindRefused}
}

func machineClosedError() error {
	return &client.UserError{Code: "machine_closed", Msg: "the session machine's creation handle is closed",
		Hint: "Use the saved host for a new operation.", Kind: client.KindRefused}
}

type createdMachine struct {
	mu        sync.Mutex
	host      client.Host
	box       *box
	closed    bool
	closing   bool
	destroyed bool
	active    int
	processes map[*createdMachineProcess]struct{}
}

// Host returns metadata only, with no capability to destroy or connect.
func (m *createdMachine) Host() client.Host {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneMachineHost(m.host)
}

// Lifecycle retains the original API client and provider locators. It never
// calls Provider.Open or resolves credentials from a returned Host.
func (m *createdMachine) Lifecycle() client.Lifecycle { return &createdMachineLifecycle{owner: m} }

func (m *createdMachine) acquire() (*box, func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.closing || m.destroyed || m.box == nil {
		return nil, nil, machineClosedError()
	}
	if !validMachineID(m.box.id) {
		return nil, nil, machineIdentityError()
	}
	m.active++
	return m.box, func() { m.mu.Lock(); m.active--; m.mu.Unlock() }, nil
}

// Rollback deletes only the sandbox returned by this actual create call. The
// caller supplies its original bounded cleanup context, possibly uncancelled.
func (m *createdMachine) Rollback(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.destroyed {
		m.mu.Unlock()
		return nil
	}
	if m.closed || m.box == nil {
		m.mu.Unlock()
		return machineClosedError()
	}
	if !validMachineID(m.box.id) {
		m.mu.Unlock()
		return machineIdentityError()
	}
	box := m.box
	m.active++
	m.mu.Unlock()
	err := box.a.kill(ctx, box.id)
	m.mu.Lock()
	m.active--
	if err == nil {
		m.destroyed = true
	}
	m.mu.Unlock()
	return err
}

// Close revokes new work. A still-owned process must first be killed and
// joined by its transport owner, so no I/O can outlive the API credentials.
func (m *createdMachine) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closing = true
	if m.active != 0 || len(m.processes) != 0 {
		return &client.UserError{Code: "machine_active", Msg: "the session machine still has an owned operation or process",
			Hint: "Close its bridge and join every owned process, then close the creation handle again.", Kind: client.KindRefused}
	}
	m.closed = true
	if m.box != nil {
		m.box.mu.Lock()
		if m.box.a != nil {
			m.box.a.key = ""
		}
		m.box.last = nil
		m.box.mu.Unlock()
		m.box = nil
	}
	return nil
}

type createdMachineLifecycle struct{ owner *createdMachine }

func (l *createdMachineLifecycle) Status(ctx context.Context) (string, error) {
	l.owner.mu.Lock()
	destroyed, closed := l.owner.destroyed, l.owner.closed
	l.owner.mu.Unlock()
	if destroyed && !closed {
		return client.MachineMissing, nil
	}
	b, done, err := l.owner.acquire()
	if err != nil {
		return "", err
	}
	defer done()
	sb, err := b.a.get(ctx, b.id)
	if errors.Is(err, errNotFound) {
		return client.MachineMissing, nil
	}
	if err != nil {
		return "", err
	}
	if sb.SandboxID != b.id {
		return "", machineIdentityError()
	}
	switch sb.State {
	case "running":
		b.remember(sb)
		return client.MachineRunning, nil
	case "paused":
		return client.MachineStopped, nil
	default:
		return client.MachineStarting, nil
	}
}

func (l *createdMachineLifecycle) Start(ctx context.Context) error {
	b, done, err := l.owner.acquire()
	if err != nil {
		return err
	}
	defer done()
	_, idle := b.Leases()
	sb, err := b.a.connect(ctx, b.id, int(idle.Seconds()))
	if err != nil {
		return err
	}
	if sb.SandboxID != b.id {
		return machineIdentityError()
	}
	b.remember(sb)
	return nil
}

func (l *createdMachineLifecycle) Stop(ctx context.Context) error {
	b, done, err := l.owner.acquire()
	if err != nil {
		return err
	}
	defer done()
	return b.a.pause(ctx, b.id)
}

func (l *createdMachineLifecycle) Destroy(ctx context.Context) error { return l.owner.Rollback(ctx) }

func (l *createdMachineLifecycle) Renew(ctx context.Context, duration time.Duration) error {
	b, done, err := l.owner.acquire()
	if err != nil {
		return err
	}
	defer done()
	return b.a.setTimeout(ctx, b.id, int(duration.Seconds()))
}

func (l *createdMachineLifecycle) Leases() (time.Duration, time.Duration) {
	l.owner.mu.Lock()
	defer l.owner.mu.Unlock()
	if l.owner.box == nil {
		return 0, 0
	}
	return l.owner.box.Leases()
}

func (l *createdMachineLifecycle) Transport(ctx context.Context, interactive bool) (client.Transport, error) {
	b, done, err := l.owner.acquire()
	if err != nil {
		return nil, err
	}
	defer done()
	b.mu.Lock()
	last := b.last
	b.mu.Unlock()
	if last == nil || last.EnvdAccessToken == "" {
		sb, err := b.a.get(ctx, b.id)
		if err != nil {
			return nil, err
		}
		if sb.SandboxID != b.id {
			return nil, machineIdentityError()
		}
		b.remember(sb)
		last = &sb
	}
	if last.SandboxID != b.id || last.EnvdAccessToken == "" {
		return nil, machineIdentityError()
	}
	transport := &Envd{URL: b.p.SandboxURL, SandboxID: b.id, Token: last.EnvdAccessToken, User: defaultUser,
		Host: b.h.Name, Stream: b.h.Machine.Stdin == "stream", HTTP: b.p.HTTP}
	return &createdMachineTransport{owner: l.owner, transport: transport}, nil
}

type createdMachineTransport struct {
	owner     *createdMachine
	transport client.Transport
}

func (t *createdMachineTransport) ReachHint() string      { return t.transport.ReachHint() }
func (*createdMachineTransport) KeepAlive() time.Duration { return 10 * time.Second }

func (t *createdMachineTransport) Start(ctx context.Context, remote string) (client.Proc, error) {
	_, done, err := t.owner.acquire()
	if err != nil {
		return nil, err
	}
	defer done()
	process, err := t.transport.Start(ctx, remote)
	if err != nil {
		return nil, err
	}
	owned := &createdMachineProcess{Proc: process, owner: t.owner}
	t.owner.mu.Lock()
	t.owner.processes[owned] = struct{}{}
	t.owner.mu.Unlock()
	return owned, nil
}

func (t *createdMachineTransport) Run(ctx context.Context, remote string, input io.Reader) ([]byte, error) {
	_, done, err := t.owner.acquire()
	if err != nil {
		return nil, err
	}
	defer done()
	return t.transport.Run(ctx, remote, input)
}

type createdMachineProcess struct {
	client.Proc
	owner *createdMachine
	once  sync.Once
	err   error
}

func (p *createdMachineProcess) Wait() error {
	p.once.Do(func() {
		p.err = p.Proc.Wait()
		p.owner.mu.Lock()
		delete(p.owner.processes, p)
		p.owner.mu.Unlock()
	})
	return p.err
}

func (*createdMachine) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<owned E2B session machine>")
}
