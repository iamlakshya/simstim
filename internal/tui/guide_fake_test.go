package tui

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

// fakeLocal is the guided setup's computer: what is installed, the SSH key
// and config, and the machines a provider makes. It records each change.
type fakeLocal struct {
	mu        sync.Mutex
	be        *fakeBackend
	has       map[string]bool // programs on PATH
	e2bKey    string
	sshHosts  []string
	key       string
	sshErr    map[string]error // ssh targets that do not work yet
	added     []string         // lines added to ~/.ssh/config
	created   []string
	createErr error
	block     bool   // Create waits for its context to end
	idleSaid  string // what SetIdle says, when set
}

func (b *fakeBackend) Local() onboard.Local {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.local == nil {
		b.local = &fakeLocal{be: b, has: map[string]bool{"ssh": true, "limactl": true, "docker": true}}
	}
	return b.local
}

func (l *fakeLocal) Places() []onboard.Place {
	l.mu.Lock()
	defer l.mu.Unlock()
	has := map[string]bool{}
	for k, v := range l.has {
		has[k] = v
	}
	return onboard.Places(onboard.Probe{
		GOOS: "darwin", Has: func(n string) bool { return has[n] }, SSHHosts: l.sshHosts, E2BKey: l.e2bKey,
		Providers: []string{onboard.Lima, onboard.Docker, onboard.E2B},
	})
}

func (l *fakeLocal) PublicKey() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.key
}

func (l *fakeLocal) HasSSHHost(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, h := range l.sshHosts {
		if h == name {
			return true
		}
	}
	return false
}

func (l *fakeLocal) AddSSHHost(lines string) error {
	l.be.record("ssh config add %q", lines)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.added = append(l.added, lines)
	return nil
}

func (l *fakeLocal) SSHWorks(_ context.Context, target string) error {
	l.be.record("ssh check %s", target)
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sshErr[target]
}

func (l *fakeLocal) Create(ctx context.Context, kind, name string, out io.Writer) (client.Host, error) {
	l.be.record("create %s %s", kind, name)
	fmt.Fprintf(out, "  made a %s machine\n", kind)
	l.mu.Lock()
	err, block := l.createErr, l.block
	l.created = append(l.created, kind+" "+name)
	l.mu.Unlock()
	if block {
		// The harness counts it as waiting, as it does a connection.
		l.be.blocked.Add(1)
		<-ctx.Done()
		l.be.blocked.Add(-1)
		l.be.record("create stopped, clean-up done")
		return client.Host{}, &client.UserError{Msg: "stopped while making " + name + "; the half-made " + kind + " machine box-" + name + " was deleted"}
	}
	if err != nil {
		return client.Host{}, err
	}
	h := client.Host{Name: name, Machine: &client.Machine{Provider: kind, ID: "box-" + name}}
	l.be.mu.Lock()
	l.be.hosts = append(l.be.hosts, h)
	if l.be.tiers == nil {
		l.be.tiers = map[string]client.TierEntry{}
	}
	l.be.tiers[name] = client.TierEntry{Name: name, Tier: client.ProviderTier(kind)}
	l.be.mu.Unlock()
	return h, nil
}

func (l *fakeLocal) SetIdle(_ context.Context, h client.Host) (string, error) {
	l.be.record("idle %s", h.Name)
	if l.idleSaid != "" {
		return l.idleSaid, nil
	}
	return "powers off after 30m idle", nil
}

func (c *fakeConn) AgentChecks(context.Context) ([]client.AgentCheck, error) {
	c.be.record("agents.check %s", c.name)
	c.be.mu.Lock()
	defer c.be.mu.Unlock()
	return append([]client.AgentCheck(nil), c.host().checks...), nil
}
