package cli

import (
	"context"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

type namedFakeProvider struct {
	name string
	m    client.Lifecycle
}

func (p namedFakeProvider) Name() string { return p.name }
func (p namedFakeProvider) Create(_ context.Context, req provider.Request) (client.Host, error) {
	return client.Host{Name: req.Name, Machine: &client.Machine{Provider: p.name, ID: p.name + "-" + req.Name}}, nil
}
func (p namedFakeProvider) Open(client.Host) (client.Lifecycle, error) { return p.m, nil }
func tierOfSaved(t *testing.T, name string) (client.Tier, client.TierEntry) {
	t.Helper()
	hosts, err := client.LoadHosts()
	if err != nil {
		t.Fatal(err)
	}
	tiers, err := client.LoadTiers(hosts)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := tiers.Entry(name)
	return tiers.Of(name), entry
}

// host create sets own for lima and docker, and shared for e2b.
func TestHostCreateProviderTiers(t *testing.T) {
	m, _ := withFake(t, nil)
	old := providers
	providers = []provider.Provider{
		namedFakeProvider{name: "lima", m: m},
		namedFakeProvider{name: "docker", m: m},
		namedFakeProvider{name: "e2b", m: m},
	}
	for _, p := range providers {
		client.RegisterProvider(p.Name(), p.Open)
	}
	t.Cleanup(func() { providers = old })

	oldIdle := setIdle
	setIdle = func(context.Context, client.Host) (string, error) { return "30m", nil }
	t.Cleanup(func() { setIdle = oldIdle })

	for _, c := range []struct {
		provider, name string
		want           client.Tier
	}{
		{"lima", "vm", client.TierOwn},
		{"docker", "cnt", client.TierOwn},
		{"e2b", "sbx", client.TierShared},
	} {
		okSaid(t, "host", "create", c.provider, c.name)
		if got, _ := tierOfSaved(t, c.name); got != c.want {
			t.Errorf("host create %s %s: tier = %s, want %s", c.provider, c.name, got, c.want)
		}
	}
}
