package client_test

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// The lines of a host's details: only those with a value, the saved ones
// first, and what the host said only when it answered.
func TestHostDetailsLines(t *testing.T) {
	text := func(d client.HostDetails) string {
		var b strings.Builder
		for _, l := range d.Lines() {
			b.WriteString(l.Label + ": " + l.Text() + "\n")
		}
		return b.String()
	}
	for _, c := range []struct {
		d    client.HostDetails
		want string
	}{
		{client.HostDetails{Host: client.Host{Name: "a", SSH: "a"}}, "Route: a\n"},
		{client.HostDetails{Host: client.Host{Name: "t", SSH: "t"}, Tier: client.TierOwn}, "Route: t\nTier: own\n"},
		{client.HostDetails{Host: client.Host{Name: "l", SSH: "l"}, Tier: client.TierShared, IDChanged: true,
			Info: &client.HostInfo{Version: "0.1.0", MemoryMax: 300 << 20, MemTotal: 512 << 10}, Sessions: 1},
			"Route: l\nTier: shared  host ID changed\nSystem: 512 KiB  reported by l\nHelper: " + brand.HostBinary + " 0.1.0\nAgents: none\nLimits: 300 MiB for sessions · 1 session\n"},
		{client.HostDetails{Host: client.Host{Name: "s", SSH: "s", Provider: &client.Provider{Script: "~/up.sh", WakeOnAttach: true}}},
			"Route: s\nProvider: ~/up.sh  wakes on attach\n"},
		{client.HostDetails{Host: client.Host{Name: "c", Machine: &client.Machine{Provider: "docker", ID: "c1", Image: "debian:stable-slim"}}},
			"Machine: docker c1  from debian:stable-slim\n"},
		{client.HostDetails{Host: client.Host{Name: "e", Machine: &client.Machine{Provider: "e2b", ID: "sb", Template: "base", IdleStop: "never"}}},
			"Machine: e2b sb  from the template base\nIdle: never\n"},
		{client.HostDetails{Host: client.Host{Name: "n"}, Info: &client.HostInfo{Version: "0.1.0", MemoryMax: 300 << 20, MemTotal: 512 << 10}, Sessions: 1},
			"System: 512 KiB\nHelper: " + brand.HostBinary + " 0.1.0\nAgents: none\nLimits: 300 MiB for sessions · 1 session\n"},
	} {
		if got := text(c.d); got != c.want {
			t.Errorf("%s: got\n%swant\n%s", c.d.Host.Name, got, c.want)
		}
	}
}

// The strings that a host sends never reach the terminal as they are: no
// control or hidden character, no escape sequence such as ESC c (a reset
// of the terminal) or OSC 52 (a write to the clipboard), no bidirectional
// control, and none longer than the details show.
func TestHostDetailsAreClean(t *testing.T) {
	dirty := "Debian\x1bc\x1b]52;c;aGk=\x07\u202e13\u200b\u009b"
	d := client.HostDetails{
		Host: client.Host{Name: "box", SSH: "dev@box\x1b[2J", Labels: map[string]string{"k": "v\x1b]0;x\x07"}},
		Info: &client.HostInfo{Distro: dirty, Arch: "amd64\r\n", Version: "0.1\x1bc", Agents: map[string]string{"claude\x1b[31m": "/x", "": "/y"},
			AgentVersions: map[string]string{"claude\x1b[31m": "2.1" + strings.Repeat("9", 500)}},
	}
	for _, l := range d.Lines() {
		s := l.Text()
		if strings.ContainsAny(s, "\x1b\x07\r\n\u202e\u200b\u009b") || len([]rune(s)) > 3*200+20 {
			t.Errorf("%s: %q", l.Label, s)
		}
	}
	if got := d.Facts()[0].Value; got != "Debianc]52;c;aGk=13 · amd64" {
		t.Errorf("System: %q", got)
	}
	if got := d.Facts()[2].Items; len(got) != 1 || !strings.HasPrefix(got[0], "claude[31m 2.1999") || len([]rune(got[0])) != len("claude[31m")+1+200 {
		t.Errorf("Agents: %q", got)
	}
}

// Two hosts whose names differ only in case keep two records, also on a
// file system that ignores case. A record names one machine: a host made
// again with the same machine ID but another stamp does not see it.
func TestRemovalRecordsApart(t *testing.T) {
	ownState(t)
	upper := client.Host{Name: "Box", Machine: &client.Machine{Provider: "lima", ID: "vm-box", Created: "1"}}
	lower := client.Host{Name: "box", Machine: &client.Machine{Provider: "lima", ID: "vm-box", Created: "1"}}
	r := client.LoadRemoval(upper)
	if err := r.Record(client.RemovalDestroy); err != nil {
		t.Fatal(err)
	}
	if client.LoadRemoval(lower).Has(client.RemovalDestroy) {
		t.Fatal("box sees the record of Box")
	}
	r = client.LoadRemoval(lower)
	if err := r.Record(client.RemovalDestroy); err != nil {
		t.Fatal(err)
	}
	if err := client.ClearRemoval("box"); err != nil {
		t.Fatal(err)
	}
	if !client.LoadRemoval(upper).Has(client.RemovalDestroy) || client.LoadRemoval(lower).Has(client.RemovalDestroy) {
		t.Fatal("the clear of box changed the record of Box")
	}
	again := upper
	again.Machine = &client.Machine{Provider: "lima", ID: "vm-box", Created: "2"}
	if client.LoadRemoval(again).Has(client.RemovalDestroy) {
		t.Error("the record of the earlier machine counts for the new one")
	}
	other := upper
	other.Machine = &client.Machine{Provider: "docker", ID: "vm-box", Created: "1"}
	if client.LoadRemoval(other).Has(client.RemovalDestroy) {
		t.Error("the record of a lima machine counts for a docker one")
	}
}

// A record of a removal is kept only for a name that can name a folder.
func TestRemovalOfABadName(t *testing.T) {
	ownState(t)
	r := client.LoadRemoval(client.Host{Name: "../x"})
	if err := r.Record(client.RemovalDestroy); err != nil {
		t.Fatal(err)
	}
	if client.LoadRemoval(client.Host{Name: "../x"}).Has(client.RemovalDestroy) {
		t.Error("a record was kept for a bad name")
	}
	if err := client.ClearRemoval("../x"); err != nil {
		t.Error(err)
	}
	ok := client.LoadRemoval(client.Host{Name: "box"})
	if err := ok.Record(client.RemovalDestroy); err != nil || !client.LoadRemoval(client.Host{Name: "box"}).Has(client.RemovalDestroy) {
		t.Fatalf("the record of box: %v", err)
	}
	if err := client.ClearRemoval("box"); err != nil || client.LoadRemoval(client.Host{Name: "box"}).Has(client.RemovalDestroy) {
		t.Errorf("the record of box after the clear: %v", err)
	}
}
