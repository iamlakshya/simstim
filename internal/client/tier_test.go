package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/version"
)

func TestTierScaleAndProviders(t *testing.T) {
	if TierUnknown >= TierShared || TierShared >= TierThrowaway || TierThrowaway >= TierOwn {
		t.Fatalf("unexpected tier order: %d %d %d %d", TierUnknown, TierShared, TierThrowaway, TierOwn)
	}
	for _, tc := range []struct {
		in   string
		want Tier
	}{
		{"own", TierOwn}, {"OWN", TierOwn}, {" throwaway ", TierThrowaway}, {"shared", TierShared},
	} {
		got, err := ParseTier(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseTier(%q) = %v, %v, want %v", tc.in, got, err, tc.want)
		}
	}
	var ue *UserError
	if _, err := ParseTier("trusted"); !errors.As(err, &ue) || ue.Code != "bad_tier" || ue.Kind != KindUsage {
		t.Errorf("ParseTier(trusted) = %v", err)
	}
	for p, want := range map[string]Tier{"lima": TierOwn, "docker": TierOwn, "e2b": TierShared, "unknown": TierShared} {
		if got := ProviderTier(p); got != want {
			t.Errorf("ProviderTier(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestLoadTiersMigrationAndHostAddedLater(t *testing.T) {
	fakeSSH(t)
	k := newKey(t, "ssh-ed25519")
	box := Host{Name: "box", SSH: "me@box"}
	cloud := Host{Name: "cloud", Machine: &Machine{Provider: "e2b", ID: "sb-1"}}
	if err := SaveHosts(context.Background(), []Host{box, cloud}); err != nil {
		t.Fatal(err)
	}
	if err := writeKeys(pinPath("box"), "box", []hostKey{k}); err != nil {
		t.Fatal(err)
	}

	hosts, err := LoadHosts()
	if err != nil {
		t.Fatal(err)
	}
	ts, err := LoadTiers(hosts)
	if err != nil {
		t.Fatal(err)
	}
	if ts.Migrated.IsZero() {
		t.Fatal("Migrated timestamp was not set")
	}
	if ts.Of("box") != TierOwn || ts.Of("cloud") != TierOwn {
		t.Fatalf("migrated tiers: box=%v cloud=%v, want own for both", ts.Of("box"), ts.Of("cloud"))
	}
	be, ok := ts.Entry("box")
	if !ok || be.Key != k.Fingerprint() {
		t.Fatalf("box entry = %+v, want key %s", be, k.Fingerprint())
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(tiersPath())
		if err != nil {
			t.Fatal(err)
		}
		if got := st.Mode().Perm(); got != 0o600 {
			t.Fatalf("tiers.toml mode = %o, want 0600", got)
		}
	}

	// Simulate an older client saving a third host to hosts.toml without writing tiers.toml.
	later := Host{Name: "later", SSH: "me@later"}
	if err := SaveHosts(context.Background(), []Host{box, cloud, later}); err != nil {
		t.Fatal(err)
	}
	hosts, err = LoadHosts()
	if err != nil {
		t.Fatal(err)
	}
	ts2, err := LoadTiers(hosts)
	if err != nil {
		t.Fatal(err)
	}
	if ts2.Has("later") {
		t.Fatal("host added after migration should have no entry in tiers.toml")
	}
	if got := ts2.Of("later"); got != TierShared {
		t.Fatalf("host with no entry after migration = %v, want shared", got)
	}
	if ts2.Of("box") != TierOwn {
		t.Fatalf("box tier changed to %v", ts2.Of("box"))
	}
}

func TestMissingOrTamperedPinStopsHostWhoseKeyIsInTiers(t *testing.T) {
	dir := fakeSSH(t)
	k1 := newKey(t, "ssh-ed25519")
	k2 := newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(k1, "trusted"))
	put(t, dir, "values", values("no"))
	shows(t, dir, k1)

	h := Host{Name: "box", SSH: "me@box"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := writeKeys(pinPath("box"), "box", []hostKey{k1}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTiers([]Host{h}); err != nil {
		t.Fatal(err)
	}

	// Delete the pin file: neither HostTransport nor connArgs may connect or re-pin silently.
	if err := os.Remove(pinPath("box")); err != nil {
		t.Fatal(err)
	}
	var ue *UserError
	if _, err := HostTransport(context.Background(), h, true); !errors.As(err, &ue) || ue.Code != "host_key_unreadable" || ue.Kind != KindRefused || !strings.Contains(ue.Msg, k1.Fingerprint()) {
		t.Fatalf("HostTransport with missing pin = %v", err)
	}
	if HasPin("box") {
		t.Fatal("missing pin file was recreated without host rekey")
	}
	if _, err := (&SSH{Host: h}).connArgs(context.Background(), false); !errors.As(err, &ue) || ue.Code != "host_key_unreadable" || ue.Kind != KindRefused {
		t.Fatalf("connArgs with missing pin = %v", err)
	}

	// Replace the pin file with another key under box's header: still refused.
	if err := writeKeys(pinPath("box"), "box", []hostKey{k2}); err != nil {
		t.Fatal(err)
	}
	if _, err := HostTransport(context.Background(), h, true); !errors.As(err, &ue) || ue.Code != "host_key_unreadable" || ue.Kind != KindRefused {
		t.Fatalf("HostTransport with mismatched pin = %v", err)
	}
	if _, err := (&SSH{Host: h}).connArgs(context.Background(), false); !errors.As(err, &ue) || ue.Code != "host_key_unreadable" || ue.Kind != KindRefused {
		t.Fatalf("connArgs with mismatched pin = %v", err)
	}
}

func TestRekeySetsTierToSharedAndRestoresMissingPin(t *testing.T) {
	dir := fakeSSH(t)
	k1 := newKey(t, "ssh-ed25519")
	k2 := newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(k2, "trusted"))
	shows(t, dir, k2)

	h := Host{Name: "box", SSH: "me@box"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if err := writeKeys(pinPath("box"), "box", []hostKey{k1}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTiers([]Host{h}); err != nil {
		t.Fatal(err)
	}

	// Remove the pin file, then rekey to k2: confirm sees k1 from tiers.toml,
	// writes k2, and sets the tier to shared.
	if err := os.Remove(pinPath("box")); err != nil {
		t.Fatal(err)
	}
	var gotOld string
	oldFP, newFP, err := Rekey(context.Background(), h, "", func(o, _ string) error {
		gotOld = o
		return nil
	})
	if err != nil {
		t.Fatalf("Rekey: %v", err)
	}
	if !strings.Contains(gotOld, k1.Fingerprint()) || oldFP != gotOld || newFP != k2.Fingerprint() {
		t.Fatalf("Rekey old=%q new=%q, want old containing %s and new=%s", oldFP, newFP, k1.Fingerprint(), k2.Fingerprint())
	}
	ts, err := LoadTiers([]Host{h})
	if err != nil {
		t.Fatal(err)
	}
	e, ok := ts.Entry("box")
	if !ok || e.Tier != TierShared || e.Key != k2.Fingerprint() {
		t.Fatalf("entry after Rekey = %+v, want shared with key %s", e, k2.Fingerprint())
	}
}

type hostIDBridge struct {
	hostID       string
	refuseAttach bool
}

func (b *hostIDBridge) ReachHint() string { return "check fake" }
func (b *hostIDBridge) Run(context.Context, string, io.Reader) ([]byte, error) {
	return []byte("Linux aarch64\n__HELPER_PRESENT__\n"), nil
}
func (b *hostIDBridge) Start(context.Context, string) (Proc, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer outW.Close()
		defer inR.Close()
		r := bufio.NewReader(inR)
		if f, err := proto.Read(r); err != nil || f.Type != proto.THello {
			return
		}
		_ = proto.NewWriter(outW).WriteJSON(proto.THello, proto.Hello{
			Magic: proto.Magic, Protocol: version.Protocol, Version: version.Version, Role: "bridge", HostID: b.hostID,
		})
		for {
			f, err := proto.Read(r)
			if err != nil {
				return
			}
			if f.Type == proto.TAttach && b.refuseAttach {
				_ = proto.NewWriter(outW).WriteJSON(proto.TError, proto.Error{
					Code: "not_found", Message: "test attach ended", Hint: "Pick another session.",
				})
			}
		}
	}()
	return &testPipeProc{in: inW, out: outR, done: done}, nil
}

type testPipeProc struct {
	in   *io.PipeWriter
	out  *io.PipeReader
	done chan struct{}
}

func (p *testPipeProc) Stdin() io.WriteCloser { return p.in }
func (p *testPipeProc) Stdout() io.Reader     { return p.out }
func (p *testPipeProc) Stderr() string        { return "" }
func (p *testPipeProc) Wait() error           { <-p.done; return nil }
func (p *testPipeProc) Kill() {
	_ = p.in.Close()
	_ = p.out.Close()
}

type staticLifecycle struct{ t Transport }

func (l staticLifecycle) Status(context.Context) (string, error)             { return MachineRunning, nil }
func (l staticLifecycle) Start(context.Context) error                        { return nil }
func (l staticLifecycle) Stop(context.Context) error                         { return nil }
func (l staticLifecycle) Destroy(context.Context) error                      { return nil }
func (l staticLifecycle) Transport(context.Context, bool) (Transport, error) { return l.t, nil }

var currentBridge = &hostIDBridge{}

func init() {
	RegisterProvider("tiertest", func(Host) (Lifecycle, error) { return staticLifecycle{t: currentBridge}, nil })
}

func TestDialHostIDFirstSightChangeAndInvalid(t *testing.T) {
	fakeSSH(t)
	const (
		id1 = "11111111111111111111111111111111"
		id2 = "22222222222222222222222222222222"
	)
	h := Host{Name: "box", Machine: &Machine{Provider: "tiertest", ID: "m1"}}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTiers([]Host{h}); err != nil {
		t.Fatal(err)
	}

	var warnings []string
	ctx := WithWarnings(context.Background(), func(code, msg, hint string) {
		b, _ := json.Marshal(map[string]string{"code": code, "msg": msg, "hint": hint})
		warnings = append(warnings, string(b))
	})

	// First sight stores id1 without changing tier or warning.
	currentBridge.hostID = id1
	c, err := Dial(ctx, h, false)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if c.HostID() != id1 || c.HostIDChanged() || len(warnings) != 0 {
		t.Fatalf("first dial: id=%q changed=%v warnings=%v", c.HostID(), c.HostIDChanged(), warnings)
	}
	ts, _ := LoadTiers([]Host{h})
	if e, _ := ts.Entry("box"); e.Tier != TierOwn || e.HostID != id1 || e.IDChanged {
		t.Fatalf("after first dial: %+v", e)
	}

	// Changed host ID warns and downgrades tier to shared.
	currentBridge.hostID = id2
	c, err = Dial(ctx, h, false)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if !c.HostIDChanged() || len(warnings) != 1 || !strings.Contains(warnings[0], "box answers with a different host ID, so its tier is now shared.") ||
		!strings.Contains(warnings[0], brand.Name+" host set box tier=own") {
		t.Fatalf("after changed ID: changed=%v warnings=%v", c.HostIDChanged(), warnings)
	}
	ts, _ = LoadTiers([]Host{h})
	if e, _ := ts.Entry("box"); e.Tier != TierShared || e.HostID != id2 || !e.IDChanged {
		t.Fatalf("entry after changed ID: %+v", e)
	}

	// SetTier clears IDChanged and raises back to own.
	if err := SetTier("box", TierOwn, "", ""); err != nil {
		t.Fatal(err)
	}
	ts, _ = LoadTiers([]Host{h})
	if e, _ := ts.Entry("box"); e.Tier != TierOwn || e.HostID != id2 || e.IDChanged {
		t.Fatalf("entry after SetTier: %+v", e)
	}

	// Malformed/hostile Hello.HostID fails with protocol error and never touches tiers.toml.
	currentBridge.hostID = "\x1bcNOT-VALID-HEX"
	var ue *UserError
	if _, err := Dial(ctx, h, false); !errors.As(err, &ue) || ue.Code != "protocol" || strings.Contains(ue.Msg, "\x1b") {
		t.Fatalf("invalid host ID dial = %v", err)
	}
	ts, _ = LoadTiers([]Host{h})
	if e, _ := ts.Entry("box"); e.HostID != id2 || e.Tier != TierOwn {
		t.Fatalf("invalid host ID modified tiers.toml: %+v", e)
	}
}

func TestUpdateHostsMigratesFirstAndRemovesOnForget(t *testing.T) {
	fakeSSH(t)
	old := Host{Name: "old", Machine: &Machine{Provider: "tiertest", ID: "m0"}}
	if err := SaveHosts(context.Background(), []Host{old}); err != nil {
		t.Fatal(err)
	}
	// Adding a new shared host before tiers.toml exists migrates old as own
	// first, and records lab as shared.
	lab := Host{Name: "lab", Machine: &Machine{Provider: "tiertest", ID: "m1"}}
	if err := UpdateHosts(context.Background(), HostChange{Kind: "host add", Now: &lab, Tier: TierShared}); err != nil {
		t.Fatal(err)
	}
	hosts, _ := LoadHosts()
	ts, err := LoadTiers(hosts)
	if err != nil {
		t.Fatal(err)
	}
	if ts.Of("old") != TierOwn || ts.Of("lab") != TierShared {
		t.Fatalf("tiers: old=%v lab=%v, want own and shared", ts.Of("old"), ts.Of("lab"))
	}

	// Forgetting lab removes its tier entry too.
	if err := UpdateHosts(context.Background(), HostChange{Kind: "host rm", Was: &lab}); err != nil {
		t.Fatal(err)
	}
	hosts, _ = LoadHosts()
	ts, err = LoadTiers(hosts)
	if err != nil {
		t.Fatal(err)
	}
	if ts.Has("lab") || ts.Of("old") != TierOwn {
		t.Fatalf("after forget: lab present=%v old=%v", ts.Has("lab"), ts.Of("old"))
	}
}
