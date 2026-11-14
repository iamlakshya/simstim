package client

import (
	"context"
	"fmt"
	"github.com/iamlakshya/simstim/internal/brand"
	"strings"
	"sync"
	"time"
)

// Machine records the VM, container or sandbox that `host create` made for a
// host. `host rm --destroy` deletes it.
type Machine struct {
	// Provider names the provider that made the machine: "lima", "docker"
	// or "e2b".
	Provider string `toml:"provider"`
	// ID names the machine as the provider knows it: the Lima instance, the
	// Docker container, or the E2B sandbox ID.
	ID string `toml:"id"`
	// User runs the helper and the sessions on docker and e2b hosts. Lima
	// hosts use the user from Lima's SSH config.
	User string `toml:"user,omitempty"`
	// Image names the container image a docker host was made from.
	Image string `toml:"image,omitempty"`
	// Template names the E2B template a sandbox was made from. It sets the
	// sandbox's CPUs and memory.
	Template string `toml:"template,omitempty"`
	// IdleStop sets how long the host may go with no client connected and no
	// session working before it stops: a duration such as "30m", or "never".
	// A Lima VM powers itself off (30m by default; it needs passwordless
	// sudo). An E2B sandbox pauses when its lease runs out (15m by default).
	IdleStop string `toml:"idle_stop,omitempty"`
	// BusyLease sets how long an E2B sandbox keeps running after the last
	// client goes, while a session is working (60m by default).
	BusyLease string `toml:"busy_lease,omitempty"`
	// TokenCommand prints the E2B API key, for example a read from your
	// keychain. It is used when E2B_API_KEY is not set and the E2B CLI is not
	// logged in. The key is never written down.
	TokenCommand string `toml:"token_command,omitempty"`
	// Stdin says how keys reach an E2B sandbox: "send" (one request for each
	// chunk, the default) or "stream" (one long request; not yet proved on
	// E2B's servers).
	Stdin string `toml:"stdin,omitempty"`
	// Created is when `host create` made the machine, as an RFC 3339 time.
	// Lima and Docker give each machine of a host the same name, so this
	// tells them apart for the record of an unfinished `host rm`.
	Created string `toml:"created,omitempty"`
}

// Machine states, as a Lifecycle reports them.
const (
	MachineRunning  = "running"
	MachineStopped  = "stopped" // stopped, exited or paused: Start brings it back
	MachineStarting = "starting"
	MachineMissing  = "missing" // the provider has no such machine
)

// Lifecycle is a provider's handle on one machine it made. The packages under
// internal/provider implement it.
type Lifecycle interface {
	// Status asks the provider for the machine's state, one of the Machine
	// constants.
	Status(ctx context.Context) (string, error)
	// Start starts or resumes the machine. It returns when the provider says
	// it runs.
	Start(ctx context.Context) error
	// Stop stops or pauses the machine.
	Stop(ctx context.Context) error
	// Destroy deletes the machine and its disk.
	Destroy(ctx context.Context) error
	// Transport reaches the running machine.
	Transport(ctx context.Context, interactive bool) (Transport, error)
}

// Leaser is a Lifecycle whose machine pauses by itself unless a client
// renews its lease, as an E2B sandbox does.
type Leaser interface {
	// Renew keeps the machine running for d from now.
	Renew(ctx context.Context, d time.Duration) error
	// Leases says how long to keep the machine running while a session works,
	// and while none does.
	Leases() (busy, idle time.Duration)
}

var (
	providersMu sync.RWMutex
	providers   = map[string]func(Host) (Lifecycle, error){}
)

// RegisterProvider makes a provider's machines reachable by name. The command
// tree registers each package under internal/provider.
func RegisterProvider(name string, open func(Host) (Lifecycle, error)) {
	providersMu.Lock()
	defer providersMu.Unlock()
	providers[name] = open
}

// OpenMachine returns the lifecycle of the machine behind h.
func OpenMachine(h Host) (Lifecycle, error) {
	if h.Machine == nil {
		return nil, &UserError{Msg: h.Name + " was not made by `host create`", Hint: brand.Name + " host rm " + h.Name + " (forgets a host you added)", Code: "not_created", Kind: KindUsage}
	}
	providersMu.RLock()
	open := providers[h.Machine.Provider]
	providersMu.RUnlock()
	if open == nil {
		return nil, &UserError{Msg: fmt.Sprintf("%s was made by the %q provider, which this build does not know", h.Name, h.Machine.Provider), Hint: "Install a release of " + brand.Name + " that knows the provider.", Code: "unknown_provider"}
	}
	return open(h)
}

// ForgetHost forgets one host through its pin and saved-entry transaction.
func ForgetHost(name string) error { return ForgetHostContext(context.Background(), name) }

// ForgetHostContext is ForgetHost with cancellation for its locks.
func ForgetHostContext(ctx context.Context, name string) error {
	was, err := SavedEntry(name)
	if err != nil {
		return err
	}
	if was == nil {
		return forgetAbsentHost(ctx, name)
	}
	return UpdateHosts(ctx, HostChange{Kind: "host rm", Was: was})
}

func forgetAbsentHost(ctx context.Context, name string) error {
	ctx, unlockHost, err := LockHost(ctx, name, "host rm")
	if err != nil {
		return err
	}
	defer unlockHost()
	if ValidHostName(name) {
		unlockPin, err := lockPin(ctx, name)
		if err != nil {
			return err
		}
		defer unlockPin()
	}
	unlockHosts, err := lockHostsFile(ctx)
	if err != nil {
		return err
	}
	defer unlockHosts()
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	for _, h := range hosts {
		if h.Name == name {
			return (HostChange{Kind: "host rm", Now: &Host{Name: name}}).changed(&h)
		}
	}
	return forgetMissingHost(ctx, name, hosts)
}

func retireMaster(ctx context.Context, h Host) error { return retireControls(ctx, h.Name) }

// CheckIdleTime checks how long a machine may idle before it stops, by the
// one rule that every idle time shares (the CLI's parseIdle, for `host
// idle` and `host create --idle-stop`): the value is trimmed, "never" in
// any case turns the stop off, and any other value is a Go duration of a
// minute or more. `config check` and the start of a host use it.
func CheckIdleTime(s string) error {
	t := strings.TrimSpace(s)
	if strings.EqualFold(t, "never") {
		return nil
	}
	if d, err := time.ParseDuration(t); err != nil || d < time.Minute {
		return &UserError{Msg: fmt.Sprintf("%q is not an idle time", s), Hint: "Use a duration of a minute or more, such as 30m or 2h, or never.", Code: "bad_value", Kind: KindUsage}
	}
	return nil
}

// Target says how h is reached, for messages: its SSH target, or its
// provider and machine.
func (h Host) Target() string {
	if h.Machine != nil && h.SSH == "" {
		return h.Machine.Provider + " " + h.Machine.ID
	}
	return h.SSH
}

type noWakeKey struct{}

// WithoutWake returns a context in which Dial does not start a stopped
// machine, but says that it is stopped. The launcher uses it for the
// connections it opens by itself.
func WithoutWake(ctx context.Context) context.Context {
	return context.WithValue(ctx, noWakeKey{}, true)
}

func wakeAllowed(ctx context.Context) bool {
	v, _ := ctx.Value(noWakeKey{}).(bool)
	return !v
}

type noInstallKey struct{}

// WithoutInstall returns a context in which Dial never installs the helper,
// and so never prunes the helpers of other versions: a missing helper is an
// error that names `host upgrade`. Commands that only read a host, such as
// `host show`, dial in it.
func WithoutInstall(ctx context.Context) context.Context {
	return context.WithValue(ctx, noInstallKey{}, true)
}

func installAllowed(ctx context.Context) bool {
	v, _ := ctx.Value(noInstallKey{}).(bool)
	return !v
}

type noInputKey struct{}

// WithoutInput returns a context in which nothing asks the user: every
// connection runs SSH with BatchMode=yes, so that SSH asks for no password,
// passphrase, host key or touch, and SSH's own messages stay out of the
// terminal. A command with input off runs in it
// (docs/contributing/cli-spec.md 6.1).
func WithoutInput(ctx context.Context) context.Context {
	return context.WithValue(ctx, noInputKey{}, true)
}

// InputAllowed reports whether something may ask the user in ctx: SSH, or a
// command of the user's own, such as a token command.
func InputAllowed(ctx context.Context) bool {
	v, _ := ctx.Value(noInputKey{}).(bool)
	return !v
}

// NewSSH is the SSH transport to h. It may ask the user (a password, a host
// key) only when interactive is set and input is allowed in ctx: with
// input off, it runs with BatchMode=yes.
func NewSSH(ctx context.Context, h Host, interactive bool) *SSH {
	return &SSH{Host: h, Interactive: interactive && InputAllowed(ctx)}
}

// route is how to reach a host: its transport, and the lifecycle of its
// machine when a provider made it.
type route struct {
	t  Transport
	lc Lifecycle
}

func routeTo(ctx context.Context, h Host, interactive bool) (route, error) {
	if err := checkHostTransportAction(ctx, h); err != nil {
		return route{}, err
	}
	interactive = interactive && InputAllowed(ctx)
	if h.Machine == nil {
		return route{t: NewSSH(ctx, h, interactive)}, nil
	}
	lc, err := OpenMachine(h)
	if err != nil {
		return route{}, err
	}
	if err := ensureRunning(ctx, h, lc); err != nil {
		return route{}, err
	}
	if err := checkHostTransportAction(ctx, h); err != nil {
		return route{}, err
	}
	t, err := lc.Transport(ctx, interactive)
	if err != nil {
		return route{}, err
	}
	if ssh, ok := t.(*SSH); ok {
		ssh.origin = &h
	}
	return route{t: t, lc: lc}, nil
}

// HostTransport returns the transport to h. A machine that a provider made
// is started first if it is stopped. A host reached over SSH gets its pin
// first, when it has none (pinRoute).
func HostTransport(ctx context.Context, h Host, interactive bool) (Transport, error) {
	r, err := routeTo(ctx, h, interactive)
	if err == nil {
		err = pinRoute(ctx, h, r.t, interactive)
	}
	return r.t, err
}

// ensureRunning asks the provider for the machine's state first, and starts
// or resumes it when it is stopped: wake on attach.
func ensureRunning(ctx context.Context, h Host, lc Lifecycle) error {
	if err := checkHostTransportAction(ctx, h); err != nil {
		return err
	}
	st, err := lc.Status(ctx)
	if err != nil {
		return err
	}
	if err := checkHostTransportAction(ctx, h); err != nil {
		return err
	}
	switch st {
	case MachineRunning:
		return nil
	case MachineMissing:
		return &UserError{
			Msg:  fmt.Sprintf("the %s machine of %s (%s) is gone", h.Machine.Provider, h.Name, h.Machine.ID),
			Hint: brand.Name + " host rm " + h.Name + " (forgets it)",
			Code: "machine_gone",
		}
	}
	if !wakeAllowed(ctx) {
		return &UserError{Msg: fmt.Sprintf("%s is %s", h.Name, st), Hint: brand.Name + " host start " + h.Name + " (an attach to one of its sessions starts it too)", Code: "host_stopped"}
	}
	if err := checkHostTransportLifecycleWake(ctx, h); err != nil {
		return err
	}
	if st != MachineStarting {
		if err := lc.Start(ctx); err != nil {
			return err
		}
	}
	if err := checkHostTransportAction(ctx, h); err != nil {
		return err
	}
	(&SSH{Host: h}).ExitMaster(ctx)
	return waitReady(ctx, h, lc, 4*time.Minute)
}

// waitReady waits until a command runs on the machine.
func waitReady(ctx context.Context, h Host, lc Lifecycle, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	var last error
	for time.Now().Before(deadline) {
		if err := checkHostTransportAction(ctx, h); err != nil {
			return err
		}
		t, err := lc.Transport(ctx, false)
		if err == nil {
			if err := checkHostTransportAction(ctx, h); err != nil {
				return err
			}
			pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			err = answers(pctx, t)
			cancel()
		}
		if err == nil || IsHostKeyError(err) {
			// A host whose key the pin refuses answers too: the
			// connection that follows says so, and host rekey can run.
			return nil
		}
		last = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return &UserError{
		Msg:  fmt.Sprintf("%s did not answer within %s of starting: %v", h.Name, limit, last),
		Hint: brand.Name + " host status " + h.Name,
		Code: "start_timeout",
	}
}

func forgetMissingHost(ctx context.Context, name string, hosts []Host) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ValidHostName(name) {
		if err := retireMaster(context.WithoutCancel(ctx), Host{Name: name}); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := forgetPin(name); err != nil {
			return err
		}
		if err := removeHostWrappers(ctx, name, hosts); err != nil {
			return err
		}
	}
	return &UserError{Msg: fmt.Sprintf("no host named %q", name), Hint: brand.Name + " host ls", Code: "host_not_found", Kind: KindNotFound}
}
