// Package e2b makes hosts that are E2B sandboxes (Firecracker microVMs). It
// reaches them through envd's process API, E2B's own authenticated door, so
// there is no SSH and no new port. A sandbox pauses with its memory when its
// lease runs out, and the client renews the lease while it is connected.
//
// It is built from E2B's public OpenAPI spec and envd's proto (both
// Apache-2.0), and tested against recorded shapes; see testdata/.
package e2b

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

// Defaults for a new sandbox.
const (
	// DefaultTemplate is the template a sandbox is made from when none is
	// named. CPUs and memory belong to the template.
	DefaultTemplate = brand.Name
	// DefaultIdleLease is how long a sandbox runs without a client while no
	// session works.
	DefaultIdleLease = 15 * time.Minute
	// DefaultBusyLease is how long it runs without a client while a session
	// works.
	DefaultBusyLease = 60 * time.Minute
	defaultUser      = "user"
)

// Provider makes E2B sandboxes. Empty fields take E2B's own servers; the
// E2B_DOMAIN, E2B_API_URL and E2B_SANDBOX_URL variables work as in E2B's
// SDKs, for a self-hosted E2B.
type Provider struct {
	// APIURL is the REST API's base URL.
	APIURL string
	// SandboxURL is envd's base URL for every sandbox.
	SandboxURL string
	// HTTP sends the requests.
	HTTP *http.Client
	// CLIConfig is the E2B CLI's login file; "" means ~/.e2b/config.json.
	CLIConfig string
}

// Name is "e2b".
func (Provider) Name() string { return "e2b" }

func domain() string {
	if d := os.Getenv("E2B_DOMAIN"); d != "" {
		return d
	}
	return "e2b.app"
}

func (p Provider) apiURL() string {
	if p.APIURL != "" {
		return p.APIURL
	}
	if u := os.Getenv("E2B_API_URL"); u != "" {
		return u
	}
	return "https://api." + domain()
}

func (p Provider) envdURL(sb sandbox) string {
	if p.SandboxURL != "" {
		return p.SandboxURL
	}
	if u := os.Getenv("E2B_SANDBOX_URL"); u != "" {
		return u
	}
	d := sb.Domain
	if d == "" {
		d = domain()
	}
	return fmt.Sprintf("https://%d-%s.%s", EnvdPort, sb.SandboxID, d)
}

func (p Provider) api(ctx context.Context, tokenCommand string) (*api, error) {
	path := p.CLIConfig
	if path == "" {
		path = cliConfigPath()
	}
	key, err := apiKey(ctx, tokenCommand, path)
	if err != nil {
		return nil, err
	}
	return &api{base: p.apiURL(), key: key, http: p.HTTP}, nil
}

// lease reads a duration from hosts.toml; "never" keeps the busy lease.
func lease(s string, def, never time.Duration) time.Duration {
	if s == "never" {
		return never
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

// BuildHint says how to build, once, a template that suits agents.
func BuildHint(name string) string {
	return fmt.Sprintf("build one once: write `FROM e2bdev/base` into e2b.Dockerfile, then run `e2b template create %s --cpu-count 2 --memory-mb 4096`", name)
}

// Create checks the template, then makes a sandbox that pauses (with its
// memory) when its lease runs out, does not wake for stray traffic, and
// keeps its URLs private.
func (p Provider) Create(ctx context.Context, req provider.Request) (client.Host, error) {
	out := req.Out
	if out == nil {
		out = io.Discard
	}
	a, err := p.api(ctx, req.TokenCommand)
	if err != nil {
		return client.Host{}, err
	}
	name := req.Template
	if name == "" {
		name = DefaultTemplate
	}
	if err := checkTemplate(ctx, a, name, out); err != nil {
		return client.Host{}, err
	}
	idle := lease(req.IdleStop, DefaultIdleLease, DefaultBusyLease)
	sb, err := a.create(ctx, newSandbox{
		TemplateID: name,
		Timeout:    int(idle.Seconds()),
		AutoPause:  true,
		AutoResume: &autoResume{Enabled: false},
		Network:    &network{AllowPublicTraffic: false},
		Metadata:   map[string]string{brand.Name + "-host": req.Name},
	})
	if err != nil {
		return client.Host{}, err
	}
	fmt.Fprintf(out, "  Created the sandbox %s from the template %s\n", sb.SandboxID, name)
	return client.Host{Name: req.Name, Machine: &client.Machine{
		Provider: "e2b", ID: sb.SandboxID, User: defaultUser, Template: name,
		IdleStop: req.IdleStop, TokenCommand: req.TokenCommand,
	}}, nil
}

func checkTemplate(ctx context.Context, a *api, name string, out io.Writer) error {
	list, err := a.templates(ctx)
	if err != nil {
		return err
	}
	t := findTemplate(list, name)
	if t == nil {
		return &client.UserError{Msg: fmt.Sprintf("your E2B team has no template called %q", name), Hint: BuildHint(name), Code: "template_not_found", Kind: client.KindNotFound}
	}
	if t.CPUCount < 2 || t.MemoryMB < 4096 {
		fmt.Fprintf(out, "  Note: the template %s has %d vCPUs and %d MiB of memory; agents want 2 vCPUs and 4 GiB or more. To fix it, %s.\n",
			name, t.CPUCount, t.MemoryMB, BuildHint(name))
	}
	return nil
}

// Open returns the lifecycle of a sandbox this provider made.
func (p Provider) Open(h client.Host) (client.Lifecycle, error) {
	return &box{p: p, h: h, id: h.Machine.ID}, nil
}

// box is one sandbox. It keeps the API key and the last answer about the
// sandbox (with its envd token) in memory only.
type box struct {
	p  Provider
	h  client.Host
	id string

	mu   sync.Mutex
	a    *api
	last *sandbox
}

func (b *box) client(ctx context.Context) (*api, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.a != nil {
		return b.a, nil
	}
	a, err := b.p.api(ctx, b.h.Machine.TokenCommand)
	if err != nil {
		return nil, err
	}
	b.a = a
	return a, nil
}

func (b *box) remember(sb sandbox) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.last = &sb
}

// Status asks the API: running or paused, or gone.
func (b *box) Status(ctx context.Context) (string, error) {
	a, err := b.client(ctx)
	if err != nil {
		return "", err
	}
	sb, err := a.get(ctx, b.id)
	if errors.Is(err, errNotFound) {
		return client.MachineMissing, nil
	}
	if err != nil {
		return "", err
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

// Start resumes the sandbox (with its memory), and leases it for the idle
// time.
func (b *box) Start(ctx context.Context) error {
	a, err := b.client(ctx)
	if err != nil {
		return err
	}
	_, idle := b.Leases()
	sb, err := a.connect(ctx, b.id, int(idle.Seconds()))
	if err != nil {
		return err
	}
	b.remember(sb)
	return nil
}

// Stop pauses the sandbox, with its memory: sessions are frozen, not killed.
func (b *box) Stop(ctx context.Context) error {
	a, err := b.client(ctx)
	if err != nil {
		return err
	}
	return a.pause(ctx, b.id)
}

// Destroy kills the sandbox. Its disk goes with it.
func (b *box) Destroy(ctx context.Context) error {
	a, err := b.client(ctx)
	if err != nil {
		return err
	}
	return a.kill(ctx, b.id)
}

// Transport reaches the sandbox's envd with the envd token from the last
// answer, fetched again if there is none.
func (b *box) Transport(ctx context.Context, _ bool) (client.Transport, error) {
	b.mu.Lock()
	last := b.last
	b.mu.Unlock()
	if last == nil || last.EnvdAccessToken == "" {
		a, err := b.client(ctx)
		if err != nil {
			return nil, err
		}
		sb, err := a.get(ctx, b.id)
		if err != nil {
			return nil, err
		}
		b.remember(sb)
		last = &sb
	}
	user := b.h.Machine.User
	if user == "" {
		user = defaultUser
	}
	return &Envd{
		URL: b.p.envdURL(*last), SandboxID: b.id, Token: last.EnvdAccessToken, User: user,
		Stream: b.h.Machine.Stdin == "stream", Host: b.h.Name, HTTP: b.p.HTTP,
	}, nil
}

// Renew sets the sandbox to pause d from now.
func (b *box) Renew(ctx context.Context, d time.Duration) error {
	a, err := b.client(ctx)
	if err != nil {
		return err
	}
	return a.setTimeout(ctx, b.id, int(d.Seconds()))
}

// Leases are the busy and idle leases from hosts.toml, or the defaults.
func (b *box) Leases() (busy, idle time.Duration) {
	busy = lease(b.h.Machine.BusyLease, DefaultBusyLease, DefaultBusyLease)
	return busy, lease(b.h.Machine.IdleStop, DefaultIdleLease, busy)
}
