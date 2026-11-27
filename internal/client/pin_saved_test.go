package client

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// sshMachine is a running machine reached over SSH, as a Lima VM is, or,
// with other, over another transport, as a Docker container is.
type sshMachine struct{ other Transport }

func (sshMachine) Status(context.Context) (string, error) { return MachineRunning, nil }
func (sshMachine) Start(context.Context) error            { return nil }
func (sshMachine) Stop(context.Context) error             { return nil }
func (sshMachine) Destroy(context.Context) error          { return nil }
func (m sshMachine) Transport(_ context.Context, interactive bool) (Transport, error) {
	if m.other != nil {
		return m.other, nil
	}
	return &SSH{Host: Host{Name: "vm", SSH: "lima-vm"}, Interactive: interactive}, nil
}

func init() {
	RegisterProvider("pintest", func(Host) (Lifecycle, error) { return sshMachine{}, nil })
	RegisterProvider("pintest-other", func(Host) (Lifecycle, error) { return sshMachine{other: &SSH{}}, nil })
}

// A host saved before pins gets its pin at its next connection, once, from
// what the user's own settings trust, and says where it came from.
func TestSavedHostGetsItsPinOnce(t *testing.T) {
	dir := fakeSSH(t)
	k := newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(k, "trusted"))
	put(t, dir, "values", values("ask"))
	shows(t, dir, k)
	h := Host{Name: "box", SSH: "box.example"}
	var notes bytes.Buffer
	ctx := WithNotices(context.Background(), &notes)

	// Not saved yet: the command that adds it pins it.
	if _, err := HostTransport(ctx, h, true); err != nil || HasPin("box") || len(sshCalls(t, dir)) != 0 {
		t.Fatalf("an unsaved host: %v, pinned %v, calls %q", err, HasPin("box"), sshCalls(t, dir))
	}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if _, err := HostTransport(ctx, h, true); err != nil || !HasPin("box") {
		t.Fatalf("a saved host: %v, pinned %v", err, HasPin("box"))
	}
	want := "box has no pinned host key yet. If your known_hosts lacks it, ssh shows the key and asks once.\n" +
		"box: host key " + k.Fingerprint() + " pinned, from your known_hosts\n"
	if notes.String() != want {
		t.Errorf("notes %q, want %q", notes.String(), want)
	}
	n := len(sshCalls(t, dir))
	if _, err := HostTransport(ctx, h, true); err != nil || len(sshCalls(t, dir)) != n || notes.String() != want {
		t.Errorf("the second connection pinned again: %v, %q", err, sshCalls(t, dir)[n:])
	}
	// A connection in the background, such as an attach's reconnect,
	// pins too, and writes nothing to the terminal.
	if err := forgetPin("box"); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(tiersPath())
	notes.Reset()
	if _, err := HostTransport(ctx, h, false); err != nil || !HasPin("box") || notes.Len() != 0 {
		t.Errorf("in the background: %v, pinned %v, notes %q", err, HasPin("box"), notes.String())
	}
}

// A saved host's key that the user's settings do not know: a pin on first
// use where the user's config takes new keys without a question, and
// otherwise, without a terminal, exit code 4. Never a connection without a
// pin.
func TestSavedHostWithoutATerminal(t *testing.T) {
	k := newKey(t, "ssh-ed25519")
	for _, c := range []struct {
		strict string
		pinned bool
	}{{"ask", false}, {"yes", false}, {"accept-new", true}, {"no", true}} {
		dir := fakeSSH(t)
		put(t, dir, "trust-log", trustLog(k, "unknown"))
		put(t, dir, "values", values(c.strict))
		shows(t, dir, k)
		h := Host{Name: "box", SSH: "box.example"}
		if err := SaveHosts(context.Background(), []Host{h}); err != nil {
			t.Fatal(err)
		}
		var notes bytes.Buffer
		_, err := Dial(WithoutInput(WithNotices(context.Background(), &notes)), h, true)
		if HasPin("box") != c.pinned || (fetches(t, dir) > 0) != c.pinned {
			t.Errorf("StrictHostKeyChecking %s: pinned %v, %d fetches", c.strict, HasPin("box"), fetches(t, dir))
		}
		var ue *UserError
		if !c.pinned && (!errors.As(err, &ue) || ue.Kind != KindNeedsAnswer || ue.Hint != brand.Name+" host rekey box --host-key SHA256:<fingerprint> --confirm box") {
			t.Errorf("StrictHostKeyChecking %s: %#v, want exit code 4", c.strict, err)
		}
		if c.pinned && !strings.Contains(notes.String(), " pinned on first use. Check it on the machine: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub\n") {
			t.Errorf("notes %q", notes.String())
		}
		// The bridge never ran without a pin.
		for _, call := range sshCalls(t, dir) {
			if strings.Contains(call, " bridge") && !hasOption(call, "StrictHostKeyChecking=yes") {
				t.Errorf("a connection without a pin: %q", call)
			}
		}
	}
}

// At a terminal, ssh asks, and a no stops the connection. A stale
// known_hosts and a revoked key stop it too, with exit code 5.
func TestSavedHostAtATerminal(t *testing.T) {
	dir := fakeSSH(t)
	k := newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(k, "unknown"))
	put(t, dir, "values", values("ask"))
	put(t, dir, "fetch-stderr", "Host key verification failed.\n")
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	var notes bytes.Buffer
	_, err := Dial(WithNotices(context.Background(), &notes), h, true)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Kind != KindCancelled || HasPin("box") || !strings.Contains(notes.String(), "box has no pinned host key yet.") {
		t.Fatalf("a no: %v, notes %q", err, notes.String())
	}
	if !hasOption(calls(t, dir, "fetch")[0], "StrictHostKeyChecking=ask") {
		t.Errorf("ssh did not ask: %q", calls(t, dir, "fetch"))
	}
	for verdict, code := range map[string]string{"changed": "host_key_changed", "revoked": "host_key_revoked"} {
		put(t, dir, "trust-log", trustLog(k, verdict))
		_, err = Dial(context.Background(), h, true)
		if !errors.As(err, &ue) || ue.Code != code || ue.Kind != KindRefused || HasPin("box") {
			t.Errorf("%s: %#v", verdict, err)
		}
		if verdict == "changed" && ue.Hint != "If you reinstalled box: "+brand.Name+" host rekey box. Otherwise, do not connect." {
			t.Errorf("changed: hint %q", ue.Hint)
		}
	}
}

// A machine that the client made gets its key on first use, without a
// question: nobody else had it. A machine reached without SSH gets none.
func TestMadeMachinePinsOnFirstUse(t *testing.T) {
	dir := fakeSSH(t)
	k := newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(k, "unknown"))
	shows(t, dir, k)
	vm := Host{Name: "vm", Machine: &Machine{Provider: "pintest", ID: "vm"}}
	box := Host{Name: "box", Machine: &Machine{Provider: "pintest-other", ID: "box"}}
	if err := SaveHosts(context.Background(), []Host{vm, box}); err != nil {
		t.Fatal(err)
	}
	var notes bytes.Buffer
	if _, err := HostTransport(WithNotices(context.Background(), &notes), vm, true); err != nil || !HasPin("vm") {
		t.Fatalf("vm: %v, pinned %v", err, HasPin("vm"))
	}
	if !hasOption(calls(t, dir, "fetch")[0], "StrictHostKeyChecking=accept-new") || notes.String() != "vm: host key "+k.Fingerprint()+" pinned on first use\n" {
		t.Errorf("vm: %q, notes %q", calls(t, dir, "fetch"), notes.String())
	}
	if _, err := HostTransport(context.Background(), box, true); err != nil || HasPin("box") {
		t.Errorf("a machine without SSH: %v, pinned %v", err, HasPin("box"))
	}
	if _, _, err := Rekey(context.Background(), box, "", func(string, string) error { return nil }); err == nil || !strings.Contains(err.Error(), "not reached over SSH") {
		t.Errorf("rekey of a machine without SSH: %v", err)
	}
}

// No ssh command connects to a saved host that has no pin yet. A machine
// that is starting is asked for its key, which logs in to nothing.
func TestNoConnectionWithoutAPin(t *testing.T) {
	dir := fakeSSH(t)
	k := newKey(t, "ssh-ed25519")
	shows(t, dir, k)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	s := &SSH{Host: h}
	ctx := context.Background()
	_, e1 := s.Command(ctx, "true")
	_, e2 := s.Shell(ctx)
	_, e3 := s.Forward(ctx, nil)
	_, e4 := GitSSHCommand(ctx, h)
	_, e5 := s.VerboseLog(ctx)
	for i, err := range []error{e1, e2, e3, e4, e5} {
		var ue *UserError
		if !errors.As(err, &ue) || ue.Code != "host_key_unpinned" || ue.Hint != brand.Name+" host test box (it pins the key first)" {
			t.Errorf("command %d: %v", i+1, err)
		}
	}
	if len(sshCalls(t, dir)) != 0 {
		t.Errorf("ssh ran: %q", sshCalls(t, dir))
	}
	// A machine that waits for SSH asks for the key only.
	start := time.Now()
	if err := waitReady(ctx, h, sshMachine{other: s}, time.Minute); err != nil || time.Since(start) > 30*time.Second {
		t.Errorf("waitReady = %v", err)
	}
	if got := sshCalls(t, dir); len(got) != 1 || !hasOption(got[0], "PreferredAuthentications=none") {
		t.Errorf("waitReady ran %q", got)
	}
	// Nor a host that is not saved: with no pin and no stage, nothing
	// connects.
	var ue *UserError
	if _, err := (&SSH{Host: Host{Name: "new", SSH: "box.example"}}).Command(ctx, "true"); !errors.As(err, &ue) || ue.Code != "host_key_unpinned" {
		t.Errorf("new: %v", err)
	}
	// Two saved hosts whose names differ only in case each get a pin of
	// their own, also on a disk that ignores case.
	twin := newKey(t, "ssh-ed25519")
	put(t, dir, "values", values("accept-new"))
	for _, c := range []struct {
		name string
		k    hostKey
	}{{"box", k}, {"BOX", twin}} {
		if err := SaveHosts(context.Background(), []Host{h, {Name: "BOX", SSH: "box.example"}}); err != nil {
			t.Fatal(err)
		}
		put(t, dir, "trust-log", trustLog(c.k, "unknown"))
		shows(t, dir, c.k)
		if _, err := HostTransport(ctx, Host{Name: c.name, SSH: "box.example"}, true); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
	}
	for name, want := range map[string]hostKey{"box": k, "BOX": twin} {
		if c, ok := PinCheck(Host{Name: name, SSH: "box.example"}); !ok || !c.OK || c.Detail != want.Fingerprint()+" pinned" {
			t.Errorf("host doctor's line for %s: %+v", name, c)
		}
	}
}

// isSaved reports whether hosts.toml holds the host called name.
func isSaved(t *testing.T, name string) bool {
	t.Helper()
	ok, err := savedHost(name)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// A changed key stops the bridge with the error of spec 14.14, a refusal,
// whatever the user's config says.
func TestChangedKeyStopsTheBridge(t *testing.T) {
	dir := fakeSSH(t)
	h := Host{Name: "box", SSH: "box.example"}
	if err := SaveHosts(context.Background(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	k := newKey(t, "ssh-ed25519")
	if err := writeTestPin("box", []hostKey{k}); err != nil {
		t.Fatal(err)
	}
	shown := "SHA256:" + strings.Repeat("Q", 43)
	put(t, dir, "stderr", "@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\r\n"+
		"The fingerprint for the ED25519 key sent by the remote host is\r\n"+shown+".\r\n"+
		"Host key for "+brand.Name+"-box has changed and you have requested strict checking.\r\nHost key verification failed.\r\n")
	_, err := Dial(context.Background(), h, false)
	var ue *UserError
	if !errors.As(err, &ue) || ue.Kind != KindRefused || ue.Msg != "box's host key changed: "+k.Fingerprint()+" was pinned, and the host shows "+shown {
		t.Fatalf("dial = %#v", err)
	}
	if _, err := (&SSH{Host: h}).Run(context.Background(), "true", nil); !IsHostKeyError(err) {
		t.Errorf("run = %v", err)
	}
	// A machine that waits for SSH counts a refused key as an answer, so
	// that host rekey can reach it, and the connection says why it failed.
	start := time.Now()
	if err := waitReady(context.Background(), h, sshMachine{other: &SSH{Host: h}}, time.Minute); err != nil || time.Since(start) > 30*time.Second {
		t.Errorf("waitReady = %v after %s", err, time.Since(start))
	}
	if c, ok := PinCheck(h); !ok || !c.OK || c.Detail != k.Fingerprint()+" pinned" {
		t.Errorf("host doctor's line: %+v", c)
	}
}

// A pin that a host add makes stays apart until the host is saved: a host
// add that fails leaves no pin, and the save decides under the lock.
func TestStagedPin(t *testing.T) {
	dir := fakeSSH(t)
	k, other := newKey(t, "ssh-ed25519"), newKey(t, "ssh-ed25519")
	put(t, dir, "trust-log", trustLog(k, "unknown"))
	shows(t, dir, k)
	h := Host{Name: "box", SSH: "box.example"}
	ctx := context.Background()

	// A host add that fails after the pin.
	if err := StagePin(ctx, "box"); err != nil {
		t.Fatal(err)
	}
	if _, err := PinHostKey(ctx, h, "", FirstUseAccept); err != nil {
		t.Fatal(err)
	}
	args, err := (&SSH{Host: h}).baseArgs()
	if err != nil || HasPin("box") || !strings.Contains(strings.Join(args, " "), ".known_hosts-"+HostFileKey("box")+"-staged-") {
		t.Fatalf("staged: %v, pin %v, args %q", err, HasPin("box"), args)
	}
	AbortPin("box")
	for f := range tree(t, ConfigDir()) {
		if strings.Contains(f, "known_hosts-box") && !strings.HasSuffix(f, ".lock") {
			t.Errorf("a failed add left %s", f)
		}
	}

	// The save moves the pin.
	if err := StagePin(ctx, "box"); err != nil {
		t.Fatal(err)
	}
	if _, err := PinHostKey(ctx, h, "", FirstUseAccept); err != nil {
		t.Fatal(err)
	}
	if err := SaveHostPin(h); err != nil || !HasPin("box") || !isSaved(t, "box") {
		t.Fatalf("save: %v", err)
	}
	if keys, _ := readPin("box"); len(keys) != 1 || keys[0].Fingerprint() != k.Fingerprint() {
		t.Errorf("pin %v", keys)
	}

	// Another client added the host first, with another key: this one
	// saves nothing, and keeps the pin that is there.
	if err := SaveHosts(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	shows(t, dir, other)
	put(t, dir, "trust-log", trustLog(other, "unknown"))
	if err := StagePin(ctx, "box"); err != nil {
		t.Fatal(err)
	}
	if _, err := PinHostKey(ctx, h, "", FirstUseAccept); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(context.Background(), []Host{{Name: "box", SSH: "first.example"}}); err != nil { // the other client's save
		t.Fatal(err)
	}
	var ue *UserError
	if err := SaveHostPin(h); !errors.As(err, &ue) || ue.Code != "host_key_pinned" || ue.Kind != KindRefused ||
		ue.Hint != "If you reinstalled box: "+brand.Name+" host rekey box. Otherwise, do not connect." {
		t.Errorf("a second add: %#v", err)
	}
	if got, _ := FindHost("box"); got.SSH != "first.example" {
		t.Errorf("the second add saved %+v", got)
	}
	if keys, _ := readPin("box"); keys[0].Fingerprint() != k.Fingerprint() {
		t.Error("the second add replaced the pin")
	}
}

// host rekey: it reads the key again, offering no key, shows both
// fingerprints, and replaces the pin only after the yes.
