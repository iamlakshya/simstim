package tui

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
	"github.com/iamlakshya/simstim/internal/onboard"
)

// Backend is everything the launcher needs from outside: saved hosts,
// SSH, the host helper and attach. The real one wraps package client. Tests
// use a fake, so every screen can be checked without SSH.
type Backend interface {
	// LoadHosts and SaveHosts read and write the saved hosts. Neither does
	// anything once ctx has ended, the command's own context: a save that a
	// cancel came before writes nothing (client.SaveHosts).
	LoadHosts(ctx context.Context) ([]client.Host, error)
	SaveHosts(ctx context.Context, hosts []client.Host) error
	Settings() client.Settings
	// Dial opens a background connection. It never prompts.
	Dial(ctx context.Context, h client.Host) (Conn, error)
	// Probe, Install and HelperSize serve the add-host wizard.
	Probe(ctx context.Context, h client.Host) (client.Platform, error)
	Install(ctx context.Context, h client.Host, p client.Platform) error
	HelperSize(p client.Platform) (int, bool)
	HelperVersion() string
	// SSHConfigHosts lists the Host aliases in the user's SSH config.
	SSHConfigHosts() []string
	// Attach runs a session in this terminal until the user detaches.
	Attach(ctx context.Context, o client.AttachOptions) (client.AttachResult, error)
	// Memory and Remember read and write the launcher's memory: Remember
	// writes the sessions, and RememberLast the session last attached to.
	Memory() memory
	Remember(map[string]client.HostList)
	RememberLast(host, id, name string)
	// Secrets fetches the values of a host's own secrets for a new
	// session. It never prompts.
	Secrets(ctx context.Context, h client.Host) (map[string]api.Secret, error)
	// SessionSecrets fetches the values of the named secrets again, for a
	// resume of a session that started with them. It never prompts.
	SessionSecrets(ctx context.Context, names []string) (map[string]api.Secret, error)
	// SetupAnswers and RememberSetup read and keep the user's answers about
	// repositories' setup scripts on a host. A yes keeps the setup hash of
	// the question that it answers. RememberSetup's warning says that the
	// answers file did not parse, and where it went. A cancelled write
	// keeps the stored answers as they were.
	SetupAnswers(host string) client.SetupAnswers
	RememberSetup(ctx context.Context, host, repo string, run bool, hash string) (*client.UserError, error)
	// Local is what the guided setup reads and changes on this computer:
	// where agents can run, SSH keys and config, and making hosts.
	Local() onboard.Local
}

// Conn is one connection to a host. *client.Conn satisfies it.
type Conn interface {
	Sessions(ctx context.Context) ([]client.Session, error)
	NewSession(ctx context.Context, a client.NewArgs) (*client.Session, error)
	Resume(ctx context.Context, a client.ResumeArgs) (*client.ResumeResult, error)
	Delete(ctx context.Context, id string) (client.DeleteResult, error)
	DeleteNamed(ctx context.Context, id, name string) (client.DeleteResult, error)
	RenameSession(ctx context.Context, id, name string) error
	Doctor(ctx context.Context) (client.HostInfo, error)
	Peek(ctx context.Context, id string, n int) ([]byte, int, int, error)
	Setup(ctx context.Context) (client.SetupResult, error)
	RoundTrip(ctx context.Context) (time.Duration, error)
	Folders(ctx context.Context, prefix string) (client.FoldersResult, error)
	AgentChecks(ctx context.Context) ([]client.AgentCheck, error)
	Done() <-chan struct{}
	Close()
}

var _ Conn = (*client.Conn)(nil)

// realBackend talks to real hosts through package client.
type realBackend struct {
	local onboard.Local
}

func (b realBackend) Local() onboard.Local {
	if b.local == nil {
		return onboard.System{}
	}
	return b.local
}

func (realBackend) LoadHosts(ctx context.Context) ([]client.Host, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return client.LoadHosts()
}

func (realBackend) SaveHosts(ctx context.Context, h []client.Host) error {
	old, _ := client.LoadHosts()
	if err := client.SaveHosts(ctx, h); err != nil {
		return err
	}
	kept := map[string]bool{}
	for _, x := range h {
		kept[x.Name] = true
	}
	for _, x := range old {
		if !kept[x.Name] {
			_ = client.CleanupHostFiles(ctx, x)
		}
	}
	return nil
}
func (realBackend) Settings() client.Settings                 { return client.LoadSettings() }
func (realBackend) LoadSettings() (client.Settings, string)   { return client.LoadSettingsChecked() }
func (realBackend) HelperVersion() string                     { return client.HelperVersion() }
func (realBackend) Memory() memory                            { return loadMemory() }
func (realBackend) Remember(lists map[string]client.HostList) { client.RememberLists(lists) }
func (realBackend) RememberLast(host, id, name string)        { RememberLast(host, id, name) }

func (realBackend) SetupAnswers(host string) client.SetupAnswers {
	return client.LoadSetupAnswers(host)
}

func (realBackend) RememberSetup(ctx context.Context, host, repo string, run bool, hash string) (*client.UserError, error) {
	return client.RememberSetupContext(ctx, host, repo, run, hash)
}

func (realBackend) Secrets(ctx context.Context, h client.Host) (map[string]api.Secret, error) {
	return client.FetchSecrets(ctx, client.LoadSettings(), client.SecretNames(h, nil), false)
}

func (realBackend) SessionSecrets(ctx context.Context, names []string) (map[string]api.Secret, error) {
	return client.FetchSecrets(ctx, client.LoadSettings(), names, false)
}

func (realBackend) Dial(ctx context.Context, h client.Host) (Conn, error) {
	// Opening the launcher must not boot every stopped VM or resume every
	// paused sandbox; it says they are stopped instead.
	c, err := client.Dial(client.WithoutWake(ctx), h, false)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Probe and Install reach the host the way every connection does: the
// system ssh, or the provider's own transport for a host that a provider
// made. They never prompt. Probe pins an SSH host's key before connecting.
func (realBackend) Probe(ctx context.Context, h client.Host) (client.Platform, error) {
	if h.SSH != "" && (h.Machine == nil || h.Machine.Provider == "lima") {
		if _, err := client.PinHostKey(ctx, h, client.PinOptions{
			AcceptFirstUse: h.Machine != nil,
			Interactive:    false,
		}); err != nil {
			return client.Platform{}, err
		}
	}
	t, err := client.HostTransport(ctx, h, false)
	if err != nil {
		return client.Platform{}, err
	}
	return client.Probe(ctx, t)
}

// removeUnsavedPin removes a host's pin file if the host is not saved in
// hosts.toml, so cancelling the add-host wizard leaves no stale pin.
func (realBackend) removeUnsavedPin(name string) {
	if name == "" {
		return
	}
	if _, err := client.FindHost(name); err != nil {
		_ = client.RemovePin(context.Background(), name)
	}
}

func (realBackend) Install(ctx context.Context, h client.Host, p client.Platform) error {
	t, err := client.HostTransport(ctx, h, false)
	if err != nil {
		return err
	}
	return client.Install(ctx, t, p)
}

func (realBackend) HelperSize(p client.Platform) (int, bool) {
	b, ok := helpers.For(p.OS, p.Arch)
	return len(b), ok
}

func (realBackend) SSHConfigHosts() []string { return client.SSHConfigHosts() }

func (realBackend) Attach(ctx context.Context, o client.AttachOptions) (client.AttachResult, error) {
	return client.Attach(ctx, o)
}

// connSet tracks every open connection, so quitting closes them all, even
// one whose dial finishes after the screen has gone.
type connSet struct {
	mu        sync.Mutex
	conns     map[Conn]struct{}
	unadopted map[Conn]struct{}
	dropped   map[Conn]struct{}
	closed    bool
}

func newConnSet() *connSet {
	return &connSet{
		conns:     map[Conn]struct{}{},
		unadopted: map[Conn]struct{}{},
		dropped:   map[Conn]struct{}{},
	}
}

// add records an already-adopted connection c. It closes c and returns false
// after closeAll.
func (s *connSet) add(c Conn) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		c.Close()
		return false
	}
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	return true
}

// addUnadopted records a freshly dialled connection before Update adopts it.
// If the program stops before Update adopts c, dropUnadopted closes it.
func (s *connSet) addUnadopted(c Conn) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		c.Close()
		return false
	}
	s.conns[c] = struct{}{}
	s.unadopted[c] = struct{}{}
	s.mu.Unlock()
	return true
}

// adopt marks c as owned by Model.hosts. It returns false if c was already
// closed or dropped before Update could adopt it.
func (s *connSet) adopt(c Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if _, wasDropped := s.dropped[c]; wasDropped {
		return false
	}
	delete(s.unadopted, c)
	s.conns[c] = struct{}{}
	return true
}

// dropUnadopted closes and forgets every connection that Update never adopted.
func (s *connSet) dropUnadopted() {
	s.mu.Lock()
	if len(s.unadopted) == 0 {
		s.mu.Unlock()
		return
	}
	stale := make([]Conn, 0, len(s.unadopted))
	for c := range s.unadopted {
		stale = append(stale, c)
		delete(s.conns, c)
		s.dropped[c] = struct{}{}
	}
	clear(s.unadopted)
	s.mu.Unlock()
	for _, c := range stale {
		c.Close()
	}
}

func (s *connSet) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// drop closes c and forgets it.
func (s *connSet) drop(c Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	delete(s.unadopted, c)
	s.mu.Unlock()
	c.Close()
}

// closeAll closes every connection, in parallel, and refuses new ones.
func (s *connSet) closeAll() {
	s.mu.Lock()
	s.closed = true
	all := make([]Conn, 0, len(s.conns))
	for c := range s.conns {
		all = append(all, c)
	}
	clear(s.conns)
	clear(s.unadopted)
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range all {
		wg.Go(c.Close)
	}
	wg.Wait()
}

func asUserError(err error) (*client.UserError, bool) {
	var ue *client.UserError
	if errors.As(err, &ue) {
		return ue, true
	}
	return nil, false
}
