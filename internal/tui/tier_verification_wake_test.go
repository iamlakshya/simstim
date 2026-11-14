package tui

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

type ownedTierLifecycle struct {
	state  string
	starts int
}

func (l *ownedTierLifecycle) Status(context.Context) (string, error) { return l.state, nil }
func (l *ownedTierLifecycle) Start(context.Context) error {
	l.starts++
	l.state = client.MachineRunning
	return nil
}
func (l *ownedTierLifecycle) Stop(context.Context) error { l.state = client.MachineStopped; return nil }
func (l *ownedTierLifecycle) Destroy(context.Context) error {
	l.state = client.MachineMissing
	return nil
}
func (*ownedTierLifecycle) Transport(context.Context, bool) (client.Transport, error) {
	return ownedTierTransport{}, nil
}

type ownedTierTransport struct{}

func (ownedTierTransport) ReachHint() string { return "owned fake transport" }
func (ownedTierTransport) Run(context.Context, string, io.Reader) ([]byte, error) {
	return nil, nil // the existing lifecycle readiness check succeeds locally
}
func (ownedTierTransport) Start(ctx context.Context, _ string) (client.Proc, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer inR.Close()
		defer outW.Close()
		r := bufio.NewReader(inR)
		if frame, err := proto.Read(r); err != nil || frame.Type != proto.THello {
			return
		}
		if err := proto.NewWriter(outW).WriteJSON(proto.THello, proto.Hello{
			Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version,
			Role: "bridge", HostID: "11111111111111111111111111111111",
		}); err != nil {
			return
		}
		for {
			if _, err := proto.Read(r); err != nil {
				return
			}
		}
	}()
	return &ownedTierProc{in: inW, out: outR, done: done}, nil
}

type ownedTierProc struct {
	in   *io.PipeWriter
	out  *io.PipeReader
	done <-chan struct{}
}

func (p *ownedTierProc) Stdin() io.WriteCloser { return p.in }
func (p *ownedTierProc) Stdout() io.Reader     { return p.out }
func (*ownedTierProc) Stderr() string          { return "" }
func (p *ownedTierProc) Wait() error           { <-p.done; return nil }
func (p *ownedTierProc) Kill()                 { _ = p.in.Close(); _ = p.out.Close() }

func TestTierVerificationPreservesBrowsingAndAttachWakeSemantics(t *testing.T) {
	for _, attach := range []bool{false, true} {
		t.Run(map[bool]string{false: "browse", true: "attach"}[attach], func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			lifecycle := &ownedTierLifecycle{state: client.MachineStopped}
			client.RegisterProvider("tier-wake-test", func(client.Host) (client.Lifecycle, error) { return lifecycle, nil })
			be := newFakeBackend()
			be.hosts = []client.Host{{Name: "box", Machine: &client.Machine{Provider: "tier-wake-test", ID: "owned"}}}
			be.tiers = map[string]client.TierEntry{"box": {Name: "box", Tier: client.TierShared, NeedsAnswer: true}}
			if err := client.SaveHosts(context.Background(), be.hosts); err != nil {
				t.Fatal(err)
			}
			pending := "migrated = 1970-01-01T00:00:01Z\n[[host]]\nname = \"box\"\ntier = \"shared\"\nneeds_answer = true\n"
			if err := os.WriteFile(filepath.Join(client.ConfigDir(), "tiers.toml"), []byte(pending), 0o600); err != nil {
				t.Fatal(err)
			}
			m := New(context.Background(), be, Options{})
			defer m.Close()
			h := m.hostByName("box")
			// The real resolver checks an owned in-process bridge and saves
			// the chosen tier. No provider command or network connection runs.
			asked := false
			be.resolveTier = func(ctx context.Context, host client.Host) (client.Tier, error) {
				ctx = client.WithTierQuestion(ctx, func(context.Context, client.Host) (client.Tier, error) {
					asked = true
					return client.TierOwn, nil
				})
				tier, err := client.ResolveHostTier(ctx, host)
				if err == nil {
					be.mu.Lock()
					be.tiers[host.Name] = client.TierEntry{Name: host.Name, Tier: tier}
					be.mu.Unlock()
				}
				return tier, err
			}
			var cmd tea.Cmd
			if attach {
				h.sessions = []client.Session{{ID: "owned-session", Name: "owned", State: "idle", Alive: true}}
				h.loaded = true
				m.screen, m.cur, m.sel = scrSessions, h.name(), sessKey{h.name(), "owned-session"}
				m.autoSel = false
				cmd = m.attachSelected()
			} else {
				m.screen, m.cur, m.hostSel = scrHosts, "", 0
				cmd = m.openSelectedHost()
			}
			if cmd == nil {
				t.Fatal("foreground action skipped its tier gate")
			}
			msg := cmd().(hostTierResolvedMsg)
			next := m.onHostTierResolved(msg)
			if attach {
				if lifecycle.starts != 1 || next == nil || msg.err != nil || !asked {
					t.Fatalf("explicit attach lost wake permission: starts=%d continued=%v err=%v", lifecycle.starts, next != nil, msg.err)
				}
			} else {
				var user *client.UserError
				if lifecycle.starts != 0 || asked || m.screen != scrHosts || !errors.As(msg.err, &user) || user.Code != "host_stopped" {
					t.Fatalf("browsing woke a stopped machine: starts=%d screen=%v err=%v", lifecycle.starts, m.screen, msg.err)
				}
			}
		})
	}
}
