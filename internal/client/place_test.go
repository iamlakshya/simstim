package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
)

func placementHostsFixture(t *testing.T) []Host {
	t.Helper()
	stateFDTestHome(t)
	t.Setenv(brand.Env("DEFAULT_HOST"), "")
	hosts := []Host{{Name: "box", SSH: "box.example"}, {Name: "other", SSH: "other.example"}, {Name: "shared", SSH: "shared.example"}}
	if err := SaveHosts(t.Context(), hosts); err != nil {
		t.Fatal(err)
	}
	for i, host := range hosts {
		tier := TierOwn
		if host.Name == "shared" {
			tier = TierShared
		}
		if err := SetTierContext(t.Context(), host.Name, tier, strings.Repeat(string(rune('a'+i)), 32), ""); err != nil {
			t.Fatal(err)
		}
	}
	return hosts
}

func TestResolveHostUsesExplicitLastEnvironmentAndSavedDefault(t *testing.T) {
	placementHostsFixture(t)
	if _, err := ResolveHost(t.Context(), ""); err == nil {
		t.Fatal("several eligible hosts silently selected one")
	}
	if err := SetDefaultHost(t.Context(), "box"); err != nil {
		t.Fatal(err)
	}
	RememberLast("other", "root2345", "last")
	for _, test := range []struct{ explicit, environment, want string }{
		{"", "", "box"}, {"", "other", "other"}, {"box", "other", "box"}, {"-", "box", "other"}, {"shared", "box", "shared"},
	} {
		t.Setenv(brand.Env("DEFAULT_HOST"), test.environment)
		host, err := ResolveHost(t.Context(), test.explicit)
		if err != nil || host.Name != test.want {
			t.Fatalf("resolve explicit=%q environment=%q: host=%q err=%v", test.explicit, test.environment, host.Name, err)
		}
	}
	t.Setenv(brand.Env("DEFAULT_HOST"), "shared")
	if _, err := ResolveHost(t.Context(), ""); err == nil {
		t.Fatal("an environment override made a shared host the default")
	}
}

func TestDefaultHostWriterPreservesUnknownDataAndRejectsSharedOrCancellation(t *testing.T) {
	placementHostsFixture(t)
	raw, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n[future]\nopaque = ['one', 'two']\n")...)
	if err := os.WriteFile(hostsPath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultHost(t.Context(), "box"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(hostsPath())
	if err != nil || !bytes.Contains(before, []byte("[future]")) || !bytes.Contains(before, []byte("two")) {
		t.Fatal("default edit lost unknown future metadata", err)
	}
	if err := SetDefaultHost(t.Context(), "shared"); err == nil {
		t.Fatal("a shared host became the saved default")
	}
	parent, cancel := context.WithCancel(t.Context())
	previous := beforeHostsSave
	defer func() { beforeHostsSave = previous }()
	beforeHostsSave = cancel
	if err := SetDefaultHost(parent, "other"); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled default edit did not preserve cancellation", err)
	}
	after, err := os.ReadFile(hostsPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused/cancelled default edit changed the file", err)
	}
	beforeHostsSave = previous
	if err := SetDefaultHost(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if got, err := DefaultHost(t.Context()); err != nil || got != "" {
		t.Fatal("clear retained a saved default", got, err)
	}
}

func TestResolveOnlyEligibleHostAndReservedNameGrammar(t *testing.T) {
	hosts := placementHostsFixture(t)
	if err := SaveHosts(t.Context(), []Host{hosts[0], hosts[2]}); err != nil {
		t.Fatal(err)
	}
	host, err := ResolveHost(t.Context(), "")
	if err != nil || host.Name != "box" {
		t.Fatal("the only non-shared host was not selected", host.Name, err)
	}
	for _, name := range []string{"auto", "-", "-box"} {
		if ValidHostName(name) {
			t.Fatal("a reserved placement word became a saved host name", name)
		}
		var streamed hostNameValidation
		for _, r := range name {
			streamed.Rune(r)
		}
		if streamed.Valid() {
			t.Fatal("streamed host grammar differs from the ordinary checker", name)
		}
	}
	for _, name := range []string{"aut", "automatic", "Auto", "box"} {
		if !ValidHostName(name) {
			t.Fatal("reserved-word check tightened unrelated host names", name)
		}
	}
}
