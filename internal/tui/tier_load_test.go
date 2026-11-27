package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestTierLoadingStopsBeforeMigrationOnBadHosts(t *testing.T) {
	for _, reload := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "reload"}[reload], func(t *testing.T) {
			ownFiles(t)
			if err := os.MkdirAll(client.ConfigDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(client.ConfigDir(), "hosts.toml"), []byte("[[host\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			load := func() (client.Tier, error) {
				if reload {
					m := &Model{ctx: context.Background(), be: realBackend{}}
					msg := m.reloadHost("box")().(hostReloadedMsg)
					return msg.tier, msg.err
				}
				hosts, err := loadSavedHosts(context.Background(), realBackend{})
				if err != nil {
					return client.TierUnknown, err
				}
				if len(hosts) != 1 {
					t.Fatalf("fixed hosts load: got %d hosts", len(hosts))
				}
				return hosts[0].tier, nil
			}
			_, err := load()
			var user *client.UserError
			if !errors.As(err, &user) || user.Code != "bad_config" {
				t.Fatalf("bad hosts did not stop tier loading: %v", err)
			}
			if _, err := os.Stat(filepath.Join(client.ConfigDir(), "tiers.toml")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed hosts load created tiers.toml: %v", err)
			}
			if err := os.Remove(filepath.Join(client.ConfigDir(), "hosts.toml")); err != nil {
				t.Fatal(err)
			}
			if err := client.SaveHosts(context.Background(), []client.Host{{Name: "box", SSH: "box"}}); err != nil {
				t.Fatal(err)
			}
			if tier, err := load(); err != nil || tier != client.TierOwn {
				t.Fatalf("fixed legacy host lost own migration: tier=%s err=%v", tier, err)
			}
		})
	}
}

func TestTierLoadingUsesTheModelContext(t *testing.T) {
	for _, reload := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial", true: "reload"}[reload], func(t *testing.T) {
			ownFiles(t)
			if err := client.SaveHosts(context.Background(), []client.Host{{Name: "box", SSH: "box"}}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var err error
			if reload {
				m := &Model{ctx: ctx, be: realBackend{}}
				if raw := m.reloadHost("box")(); raw != nil {
					msg := raw.(hostReloadedMsg)
					err = msg.err
					if msg.host != nil || msg.tier != client.TierUnknown {
						t.Fatalf("failed reload invented a host tier: %+v", msg)
					}
				} else {
					err = ctx.Err()
				}
			} else {
				m := New(ctx, realBackend{}, Options{})
				err = m.loadErr
				if len(m.hosts) != 0 {
					t.Fatalf("failed initial load invented host tiers: %+v", m.hosts)
				}
				m.Close()
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("model tier loading lost cancellation: %v", err)
			}
			if _, err := os.Stat(filepath.Join(client.ConfigDir(), "tiers.toml")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled model created tiers.toml: %v", err)
			}
		})
	}
}

func TestDemoTierLoadingHonoursCancellation(t *testing.T) {
	be := newDemoBackend(false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := be.LoadTiers(ctx, []client.Host{{Name: "new"}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("demo tier load ignored cancellation: %v", err)
	}
	if _, ok := be.tiers["new"]; ok {
		t.Fatal("cancelled demo tier load added a row")
	}
}
