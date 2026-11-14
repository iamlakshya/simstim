package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
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

// host add --yes without --tier fails with exit code 4 before it connects,
// and names --tier (isolation spec 14.8.3, question 50).
func TestHostAddYesWithoutTierExitsWith4(t *testing.T) {
	th := keyHost(t)
	_ = os.Remove(filepath.Join(th.h.Home, "ssh.log"))

	got, code := lineErr(t, "host", "add", "added", "box", "--yes")
	if code != 4 || !strings.Contains(got, "--tier") || !strings.Contains(got, "Try: "+brand.Name+" host add added box --yes --tier <tier>") {
		t.Fatalf("host add --yes without --tier: exit %d\n%s", code, got)
	}
	if _, err := os.Stat(filepath.Join(th.h.Home, "ssh.log")); err == nil {
		t.Error("host add --yes without --tier connected to the host")
	}

	// Also when input is on at a terminal: --yes still requires --tier.
	var out bytes.Buffer
	p := testPrompts("1\n", &out)
	p.line, p.flagsAt = []string{"host", "add", "added", "box", "--yes"}, 5
	err := addHostWithTier(context.Background(), p, client.Host{Name: "added", SSH: "box"}, true, "", client.TierUnknown, true)
	var ue *client.UserError
	if !errors.As(err, &ue) || exitCode(ue.Kind) != 4 || !strings.Contains(ue.Hint, "--tier <tier>") || out.Len() != 0 {
		t.Fatalf("at a terminal with --yes: %+v, wrote %q", err, out.String())
	}

	// A bad --tier fails with exit code 2 before connecting.
	if got, code := lineErr(t, "host", "add", "added", "box", "--tier", "borrowed", "--yes"); code != 2 || !strings.Contains(got, `"borrowed" is not a host tier`) {
		t.Errorf("bad --tier: exit %d\n%s", code, got)
	}

	// Without --yes and with input off: fails at the tier question and leaves no pin.
	got, code = lineErr(t, "host", "add", "added", "box")
	if code != 4 || !strings.Contains(got, "needs the tier of added") || !strings.Contains(got, "--tier <tier>") {
		t.Errorf("host add with input off: exit %d\n%s", code, got)
	}
	if files := pinFiles(t); len(files) != 0 {
		t.Errorf("a failed host add left %v", files)
	}
}

// At a terminal, host add asks who else has root on the host, has no
// default answer, and saves the chosen tier, host ID and pinned key.
func TestHostAddInteractiveTierQuestion(t *testing.T) {
	th := keyHost(t)
	fp := hostShows(t, th.h.Home, "trusted")

	// Empty Enter is not accepted, then "2" picks shared, and Enter skips
	// the agent checklist.
	var out bytes.Buffer
	p := testPrompts("\n2\n\n", &out)
	captureOutput(t, func() {
		if err := addHostWithTier(context.Background(), p, client.Host{Name: "lab", SSH: "box"}, false, "", client.TierUnknown, true); err != nil {
			t.Fatalf("addHostWithTier: %v\n%s", err, out.String())
		}
	})
	if !strings.Contains(out.String(), "Who else has root on lab?") || !strings.Contains(out.String(), "Type 1, 2 or 3, or own, shared or throwaway.") {
		t.Errorf("tier prompt output:\n%s", out.String())
	}
	gotTier, entry := tierOfSaved(t, "lab")
	if gotTier != client.TierShared || entry.Key != fp || !client.ValidHostID(entry.HostID) {
		t.Errorf("saved tier = %s, entry = %+v (want key %s)", gotTier, entry, fp)
	}

	// Three answers that are not a tier cancel.
	var out2 bytes.Buffer
	p2 := testPrompts("\nnope\n4\n", &out2)
	if _, err := p2.askTier("other"); !isCancel(err) {
		t.Errorf("three invalid answers: %v", err)
	}
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

// setup asks the tier question for an SSH host, requires --tier with --yes,
// and uses the provider's tier for --create.
func TestSetupHostTiers(t *testing.T) {
	th := keyHost(t)
	hostShows(t, th.h.Home, "trusted")

	if got, code := lineErr(t, "setup", "--host", "dev@box", "--name", "srv", "--yes", "--no-login"); code != 4 || !strings.Contains(got, "--tier <tier>") {
		t.Fatalf("setup --host --yes without --tier: exit %d\n%s", code, got)
	}
	okSaid(t, "setup", "--host", "dev@box", "--name", "srv", "--tier", "throwaway", "--agents", "none", "--yes", "--no-login")
	if got, _ := tierOfSaved(t, "srv"); got != client.TierThrowaway {
		t.Errorf("setup --host --tier throwaway: tier = %s", got)
	}

	// Interactive setup on an SSH host asks who else has root on it.
	var out bytes.Buffer
	g := newGuide(context.Background(), guideOpts{host: "dev@box", name: "shared-srv", agents: "none", agentsSet: true, noLogin: true}, onboard.System{Providers: providers})
	g.in, g.out, g.tty = strings.NewReader("2\n"), &out, true
	captureOutput(t, func() {
		if err := g.run(); err != nil {
			t.Fatalf("interactive setup: %v\n%s", err, out.String())
		}
	})
	if got, _ := tierOfSaved(t, "shared-srv"); got != client.TierShared || !strings.Contains(out.String(), "Who else has root on shared-srv?") {
		t.Errorf("interactive setup: tier = %s\n%s", got, out.String())
	}
}

// host set tier=: lowering works at once; raising connects to check the
// pinned key and requires the typed host name or --confirm <host>.
func TestHostSetTier(t *testing.T) {
	th := keyHost(t)
	fp := hostShows(t, th.h.Home, "trusted")
	okSaid(t, "host", "add", "srv", "box", "--tier", "own", "--yes")

	// Lowering works at once without connecting or asking.
	_ = os.Remove(filepath.Join(th.h.Home, "ssh.log"))
	if said := okSaid(t, "host", "set", "srv", "tier=throwaway"); said != "srv: tier own → throwaway.\n" {
		t.Errorf("lower to throwaway said %q", said)
	}
	if said := okSaid(t, "host", "set", "srv", "tier=shared"); said != "srv: tier throwaway → shared.\n" {
		t.Errorf("lower to shared said %q", said)
	}
	if _, err := os.Stat(filepath.Join(th.h.Home, "ssh.log")); err == nil {
		t.Error("lowering the tier connected to the host")
	}

	// Raising with input off and no --confirm fails with exit code 4 before connecting.
	got, code := lineErr(t, "host", "set", "srv", "tier=own")
	if code != 4 || !strings.Contains(got, "Try: "+brand.Name+" host set srv tier=own --confirm srv") {
		t.Fatalf("raise with input off: exit %d\n%s", code, got)
	}
	if _, err := os.Stat(filepath.Join(th.h.Home, "ssh.log")); err == nil {
		t.Error("raise with input off connected before failing")
	}

	// --confirm with another name fails with exit code 2.
	if got, code := lineErr(t, "host", "set", "srv", "tier=own", "--confirm", "other"); code != 2 || !strings.Contains(got, "--confirm names other") {
		t.Errorf("wrong --confirm: exit %d\n%s", code, got)
	}

	// Raising when the pinned host key changed fails with exit code 5.
	shown := "SHA256:" + strings.Repeat("Z", 43)
	changed := "@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\r\n" +
		"The fingerprint for the ED25519 key sent by the remote host is\r\n" + shown + ".\r\n" +
		"Host key for " + brand.Name + "-srv has changed and you have requested strict checking.\r\nHost key verification failed.\r\n"
	if err := os.WriteFile(filepath.Join(th.h.Home, "changed"), []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, code := lineErr(t, "host", "set", "srv", "tier=own", "--confirm", "srv"); code != 5 || !strings.Contains(got, fp+" was pinned") {
		t.Errorf("raise with changed key: exit %d\n%s", code, got)
	}
	if cur, _ := tierOfSaved(t, "srv"); cur != client.TierShared {
		t.Errorf("tier changed after refused key: %s", cur)
	}
	_ = os.Remove(filepath.Join(th.h.Home, "changed"))

	// At a terminal: Enter keeps shared (exit 130), typing srv raises it.
	var buf bytes.Buffer
	if err := setHostWith(context.Background(), testPrompts("\n", &buf), &buf, "srv", []string{"tier=own"}, ""); !isCancel(err) {
		t.Errorf("Enter when raising: %v", err)
	}
	if cur, _ := tierOfSaved(t, "srv"); cur != client.TierShared {
		t.Errorf("tier after Enter: %s", cur)
	}

	buf.Reset()
	if err := setHostWith(context.Background(), testPrompts("srv\n", &buf), &buf, "srv", []string{"tier=throwaway"}, ""); err != nil {
		t.Fatalf("typed name when raising: %v\n%s", err, buf.String())
	}
	if cur, _ := tierOfSaved(t, "srv"); cur != client.TierThrowaway {
		t.Errorf("tier after typed name: %s", cur)
	}

	// Raising with --confirm works in a script.
	said := okSaid(t, "host", "set", "srv", "tier=own", "--confirm", "srv")
	if !strings.Contains(said, "Checking srv (box)…\nsrv: tier throwaway → own.\n") {
		t.Errorf("raise with --confirm said %q", said)
	}
	if cur, _ := tierOfSaved(t, "srv"); cur != client.TierOwn {
		t.Errorf("tier after --confirm: %s", cur)
	}
}

// A changed host ID warns clearly, downgrades the host's tier to shared, and
// shows in host show and host ls; host rekey also sets the tier to shared.
func TestChangedHostIDAndRekeyDowngradeToShared(t *testing.T) {
	th := keyHost(t)
	hostShows(t, th.h.Home, "trusted")
	okSaid(t, "host", "add", "srv", "box", "--tier", "own", "--yes")

	// Replace the host's host-id file so the next bridge hello carries a new ID.
	idFile := filepath.Join(th.h.Home, ".local", "state", brand.Name, "host-id")
	if err := os.WriteFile(idFile, []byte("fedcba9876543210fedcba9876543210\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runApart(t, "host", "show", "srv")
	if err != nil {
		t.Fatalf("host show: %v\n%s\n%s", err, out, errOut)
	}
	if !strings.Contains(errOut, "Warning: srv answers with a different host ID, so its tier is now shared.") ||
		!strings.Contains(errOut, "Try: If it is the same machine: "+brand.Name+" host set srv tier=own") {
		t.Errorf("warning on stderr:\n%s", errOut)
	}
	for _, want := range []string{"  Tier       shared  host ID changed\n", "  reported by srv\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("host show lacks %q:\n%s", want, out)
		}
	}

	// With --json on another host-ID change, stderr gets a JSON warning line
	// and stdout gets tier=shared and host_id_changed=true.
	okSaid(t, "host", "set", "srv", "tier=own", "--confirm", "srv")
	if err := os.WriteFile(idFile, []byte("11112222333344445555666677778888\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	jOut, jErr, err := runApart(t, "host", "show", "srv", "--json")
	if err != nil {
		t.Fatalf("host show --json: %v\n%s\n%s", err, jOut, jErr)
	}
	var warnLine struct {
		V       int
		Warning map[string]string
	}
	if json.Unmarshal([]byte(jErr), &warnLine) != nil || warnLine.Warning["code"] != "host_id_changed" {
		t.Errorf("JSON warning = %q", jErr)
	}
	var showObj map[string]any
	if json.Unmarshal([]byte(jOut), &showObj) != nil || showObj["tier"] != "shared" || showObj["host_id_changed"] != true {
		t.Errorf("host show --json = %s", jOut)
	}

	// host ls shows the tier column and JSON field.
	lsText := ok(t, "host", "ls")
	if !strings.Contains(lsText, "srv              shared     box\n") {
		t.Errorf("host ls:\n%s", lsText)
	}
	var lsJSON []map[string]any
	if err := json.Unmarshal([]byte(okJSON(t, "host", "ls", "--json")), &lsJSON); err != nil ||
		!slices.ContainsFunc(lsJSON, func(m map[string]any) bool { return m["name"] == "srv" && m["tier"] == "shared" }) {
		t.Errorf("host ls --json = %v, %v", lsJSON, err)
	}

	// Raise back to own, then host rekey with a new key: tier becomes shared
	// and tiers.toml gets the new key.
	okSaid(t, "host", "set", "srv", "tier=own", "--confirm", "srv")
	newFP := hostShows(t, th.h.Home, "changed")
	okSaid(t, "host", "rekey", "srv", "--confirm", "srv")
	if got, entry := tierOfSaved(t, "srv"); got != client.TierShared || entry.Key != newFP {
		t.Errorf("after rekey: tier = %s, entry = %+v, want key %s", got, entry, newFP)
	}

	// Removing known_hosts-<key> while tiers.toml still holds key refuses to connect.
	pin := filepath.Join(client.ConfigDir(), "ssh", "known_hosts-"+client.HostFileKey("srv"))
	if err := os.Remove(pin); err != nil {
		t.Fatal(err)
	}
	if got, code := lineErr(t, "host", "test", "srv"); code != 5 || !strings.Contains(got, "missing") {
		t.Errorf("missing pin file: exit %d\n%s", code, got)
	}
}
