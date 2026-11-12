package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// Machine records the VM, container or sandbox that `host create` made for a
// host. `host destroy` deletes it.
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
		return nil, &UserError{Msg: h.Name + " was not made by `host create`", Hint: brand.Name + " host rm " + h.Name + " (forgets a host you added)", Code: "not_created"}
	}
	providersMu.RLock()
	open := providers[h.Machine.Provider]
	providersMu.RUnlock()
	if open == nil {
		return nil, &UserError{Msg: fmt.Sprintf("%s was made by the %q provider, which this build does not know", h.Name, h.Machine.Provider), Hint: "Install a release of " + brand.Name + " that knows the provider.", Code: "unknown_provider"}
	}
	return open(h)
}

// ForgetHost removes a host from hosts.toml, and its generated SSH config.
func ForgetHost(name string) error {
	hosts, err := LoadHosts()
	if err != nil {
		return err
	}
	out := hosts[:0]
	found := false
	for _, h := range hosts {
		if h.Name == name {
			found = true
			continue
		}
		out = append(out, h)
	}
	if !found {
		return &UserError{Msg: fmt.Sprintf("no host named %q", name), Hint: brand.Name + " host ls", Code: "host_not_found", Kind: KindNotFound}
	}
	if err := SaveHosts(out); err != nil {
		return err
	}
	if err := os.Remove(wrapperPath(Host{Name: name})); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove the generated SSH config: %w", err)
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

// route is how to reach a host: its transport, and the lifecycle of its
// machine when a provider made it.
type route struct {
	t  Transport
	lc Lifecycle
}

func routeTo(ctx context.Context, h Host, interactive bool) (route, error) {
	if h.Machine == nil {
		return route{t: &SSH{Host: h, Interactive: interactive}}, nil
	}
	lc, err := OpenMachine(h)
	if err != nil {
		return route{}, err
	}
	if err := ensureRunning(ctx, h, lc); err != nil {
		return route{}, err
	}
	t, err := lc.Transport(ctx, interactive)
	if err != nil {
		return route{}, err
	}
	return route{t: t, lc: lc}, nil
}

// HostTransport returns the transport to h. A machine that a provider made
// is started first if it is stopped.
func HostTransport(ctx context.Context, h Host, interactive bool) (Transport, error) {
	r, err := routeTo(ctx, h, interactive)
	return r.t, err
}

// ensureRunning asks the provider for the machine's state first, and starts
// or resumes it when it is stopped: wake on attach.
func ensureRunning(ctx context.Context, h Host, lc Lifecycle) error {
	st, err := lc.Status(ctx)
	if err != nil {
		return err
	}
	switch st {
	case MachineRunning:
		return nil
	case MachineMissing:
		return &UserError{
			Msg:  fmt.Sprintf("the %s machine of %s (%s) is gone", h.Machine.Provider, h.Name, h.Machine.ID),
			Hint: brand.Name + " host destroy " + h.Name + " (forgets it)",
			Code: "machine_gone",
		}
	}
	if !wakeAllowed(ctx) {
		return &UserError{Msg: fmt.Sprintf("%s is %s", h.Name, st), Hint: brand.Name + " host start " + h.Name + " (an attach to one of its sessions starts it too)", Code: "host_stopped"}
	}
	if st != MachineStarting {
		if err := lc.Start(ctx); err != nil {
			return err
		}
	}
	(&SSH{Host: h}).ExitMaster(ctx)
	return waitReady(ctx, h, lc, 4*time.Minute)
}

// waitReady waits until a command runs on the machine.
func waitReady(ctx context.Context, h Host, lc Lifecycle, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	var last error
	for time.Now().Before(deadline) {
		t, err := lc.Transport(ctx, false)
		if err == nil {
			pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			_, err = t.Run(pctx, "true", nil)
			cancel()
		}
		if err == nil {
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
