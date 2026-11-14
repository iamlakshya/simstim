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
	// LoadTiers reads the current policy alongside the saved host snapshot.
	LoadTiers(ctx context.Context, hosts []client.Host) (client.Tiers, error)
	// ResolveHostTier asks the foreground question through ctx, then saves it.
	ResolveHostTier(ctx context.Context, h client.Host) (client.Tier, error)
	// UpdateHosts saves or forgets one host, as client.UpdateHosts does:
	// only while its saved entry is still the one that the launcher read,
	// and while no command holds the lock of the host. A forget also
	// deletes the record of an unfinished `host rm` of the host, under the
	// same lock: warn is the error of that clean-up, which does not undo
	// the forget.
	UpdateHosts(c client.HostChange) (warn, err error)
	Settings() client.Settings
	// Dial opens a background connection. It never prompts.
	Dial(ctx context.Context, h client.Host) (Conn, error)
	// StagePin, PinHostKey, SaveHostPin and AbortPin pin a host that the
	// add-host wizard adds, as host add does: the pin is staged before the
	// first connection, and saved with the host.
	StagePin(ctx context.Context, name string) error
	// PinHostKey pins h's key in its stage: the key that the user's own
	// settings trust, or the one with the fingerprint want. It never asks:
	// a key that nothing names fails with *client.UncheckedKeyError.
	PinHostKey(ctx context.Context, h client.Host, want string) (client.Pin, error)
	SaveHostPin(h client.Host) error
	AbortPin(name string)
	// ForgetHost removes a host from the list, with its pin, under the
	// pin's lock, as host rm does.
	ForgetHost(ctx context.Context, name string) error
	// Probe, Install and HelperSize serve the add-host wizard.
	Probe(ctx context.Context, h client.Host) (client.Platform, error)
	Install(ctx context.Context, h client.Host, p client.Platform) error
	HelperSize(p client.Platform) (int, bool)
	HelperVersion() string
	// SSHConfigHosts lists the Host aliases in the user's SSH config.
	SSHConfigHosts() []string
	// Attach runs a session in this terminal until the user detaches.
	Attach(ctx context.Context, o client.AttachOptions) (client.AttachResult, error)
	// Memory and Remember read and write the launcher's memory, the session
	// cache: Remember writes the lists of the hosts that answered, and
	// RememberLast the session last attached to.
	Memory() memory
	Remember(lists map[string]client.HostList)
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
	// DeleteNamed deletes the session id while its name is still name.
	DeleteNamed(ctx context.Context, id, name string) (client.DeleteResult, error)
	RenameSession(ctx context.Context, id, name string) error
	Doctor(ctx context.Context) (client.HostInfo, error)
	Peek(ctx context.Context, id string, n int) ([]byte, int, int, error)
	Setup(ctx context.Context) (client.SetupResult, error)
	RoundTrip(ctx context.Context) (time.Duration, error)
	Folders(ctx context.Context, prefix string) (client.FoldersResult, error)
	AgentChecks(ctx context.Context) ([]client.AgentCheck, error)
	HostID() string
	HostIDChanged() bool
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

func (realBackend) LoadTiers(ctx context.Context, hosts []client.Host) (client.Tiers, error) {
	return client.LoadTiersContext(ctx, hosts)
}

func (realBackend) ResolveHostTier(ctx context.Context, h client.Host) (client.Tier, error) {
	return client.ResolveHostTier(ctx, h)
}

// UpdateHosts holds the lock of the host from the change through the
// clean-up of its record, so no command of the host comes between them:
// the clean-up never deletes the record of a host made again.
func (b realBackend) UpdateHosts(c client.HostChange) (warn, err error) {
	return b.UpdateHostsContext(context.Background(), c)
}

func (realBackend) UpdateHostsContext(ctx context.Context, c client.HostChange) (warn, err error) {
	name := changedName(c)
	ctx, unlock, err := client.LockHost(ctx, name, c.Kind)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := client.UpdateHosts(ctx, c); err != nil {
		return nil, err
	}
	if c.Now != nil {
		return nil, nil
	}
	afterLauncherForget(name)
	return client.ClearRemoval(name), nil
}

// afterLauncherForget runs between the launcher's forget and the clean-up
// of its record. Tests try another command there.
var afterLauncherForget = func(string) {}

// changedName is the name of the host that c changes.
func changedName(c client.HostChange) string {
	if c.Now != nil {
		return c.Now.Name
	}
	if c.Was != nil {
		return c.Was.Name
	}
	return ""
}
func (realBackend) Settings() client.Settings                 { return client.LoadSettings() }
func (realBackend) HelperVersion() string                     { return client.HelperVersion() }
func (realBackend) Memory() memory                            { return client.LoadSessionCache() }
func (realBackend) Remember(lists map[string]client.HostList) { client.RememberLists(lists) }
func (realBackend) RememberLast(host, id, name string)        { client.RememberLast(host, id, name) }

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
	c, err := client.Dial(client.WithTierQuestion(client.WithoutWake(ctx), nil), h, false)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (realBackend) StagePin(ctx context.Context, name string) error {
	return client.StagePin(ctx, name)
}
func (realBackend) StagePinOwned(ctx context.Context, name string) (context.Context, func(), error) {
	return client.StagePinOwned(ctx, name)
}
func (realBackend) SaveHostPinContext(ctx context.Context, h client.Host) error {
	return client.SaveHostPinContext(ctx, h)
}
func (realBackend) SaveHostPin(h client.Host) error { return client.SaveHostPin(h) }
func (realBackend) AbortPin(name string)            { client.AbortPin(name) }
func (realBackend) ForgetHost(ctx context.Context, name string) error {
	return client.ForgetHostContext(ctx, name)
}

func (realBackend) PinHostKey(ctx context.Context, h client.Host, want string) (client.Pin, error) {
	return client.PinHostKey(client.WithoutInput(ctx), h, want, client.FirstUseRefuse)
}

// Probe and Install reach the host the way every connection does: the
// system ssh, or the provider's own transport for a host that a provider
// made. They never prompt.
func (realBackend) Probe(ctx context.Context, h client.Host) (client.Platform, error) {
	t, err := client.HostTransport(ctx, h, false)
	if err != nil {
		return client.Platform{}, err
	}
	return client.Probe(ctx, t)
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
	mu          sync.Mutex
	conns       map[Conn]struct{}
	unadopted   map[Conn]struct{}
	dropped     map[Conn]struct{}
	closed      bool
	cleanup     []func()
	cleanupDone chan struct{}
}

func newConnSet() *connSet {
	return &connSet{
		conns:       map[Conn]struct{}{},
		cleanupDone: make(chan struct{}),
		unadopted:   map[Conn]struct{}{},
		dropped:     map[Conn]struct{}{},
	}
}

// onClose keeps cleanup owned even when a command's result is dropped.
func (s *connSet) onClose(cleanup func()) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cleanup()
		return
	}
	s.cleanup = append(s.cleanup, cleanup)
	s.mu.Unlock()
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
	first := !s.closed
	s.closed = true
	cleanup := s.cleanup
	s.cleanup = nil
	all := make([]Conn, 0, len(s.conns))
	for c := range s.conns {
		all = append(all, c)
	}
	clear(s.conns)
	clear(s.unadopted)
	s.mu.Unlock()
	if first {
		go func() {
			var wg sync.WaitGroup
			for _, close := range cleanup {
				wg.Go(close)
			}
			wg.Wait()
			close(s.cleanupDone)
		}()
	}
	var wg sync.WaitGroup
	for _, c := range all {
		wg.Go(c.Close)
	}
	wg.Wait()
}

// pinCleanupGrace stays below the existing 500 ms close-response budget.
const pinCleanupGrace = 200 * time.Millisecond

// finishCleanup lets lock-free cleanup finish without making Close wait on
// another command's pin lock. Run joins the rest after restoring the terminal.
func (s *connSet) finishCleanup() {
	select {
	case <-s.cleanupDone:
	case <-time.After(pinCleanupGrace):
	}
}

func (s *connSet) waitCleanup() { <-s.cleanupDone }

func asUserError(err error) (*client.UserError, bool) {
	var ue *client.UserError
	if errors.As(err, &ue) {
		return ue, true
	}
	return nil, false
}
