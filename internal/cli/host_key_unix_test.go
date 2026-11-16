//go:build unix

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/helpers"
)

// keyHost is the test host with its helper installed, for host add.
func keyHost(t *testing.T) *testHost {
	t.Helper()
	th := newTestHost(t)
	th.h.Install(client.HelperVersion(), "")
	return th
}

// host add pins the key and says where the pin came from; every ssh
// command after it checks the pin.
func TestHostAddPinsTheHostKey(t *testing.T) {
	th := keyHost(t)
	fp := hostShows(t, th.h.Home, "trusted")
	out := okSaid(t, "host", "add", "added", "box", "--tier", "own", "--yes")
	want := "  ✓ host key " + fp + " pinned, from your known_hosts\n"
	if !strings.Contains(out, want) || strings.Index(out, "✓ connected") > strings.Index(out, want) {
		t.Fatalf("host add said:\n%s", out)
	}
	if pinnedAdded(t) != fp {
		t.Errorf("pinned %s, want %s", pinnedAdded(t), fp)
	}
	pin := filepath.Join(client.ConfigDir(), "ssh", "known_hosts-"+client.HostFileKey("added"))
	for _, l := range sshLines(t, th) {
		if !strings.Contains(l, " -- box ") || strings.Contains(l, " PreferredAuthentications=none ") || strings.Contains(l, " -G ") {
			continue // the connections that read the key, and ssh -G
		}
		for _, o := range []string{"HostKeyAlias=" + brand.Name + "-added", "StrictHostKeyChecking=yes", "GlobalKnownHostsFile=/dev/null", "LogLevel=ERROR"} {
			if !strings.Contains(" "+l+" ", " -o "+o+" ") {
				t.Errorf("ssh ran without -o %s after the pin: %q", o, l)
			}
		}
		if !strings.Contains(l, "UserKnownHostsFile="+pin) && !strings.Contains(l, ".known_hosts-"+client.HostFileKey("added")+"-staged-") {
			t.Errorf("ssh ran without the pin: %q", l)
		}
	}
	// Again, with the same target: the pin stays.
	if out := okSaid(t, "host", "add", "added", "box", "--yes"); !strings.Contains(out, "  ✓ host key "+fp+" already pinned\n") {
		t.Errorf("host add again said:\n%s", out)
	}
	// A --host-key that the pin lacks is a refusal: only host rekey changes
	// a pin.
	_, other := hostKey(t)
	got, code := lineErr(t, "host", "add", "added", "box", "--yes", "--host-key", other)
	if code != 5 || !strings.Contains(got, "added has a pinned host key already, "+fp+", and the host shows "+other) ||
		!strings.Contains(got, "Try: If you reinstalled added: "+brand.Name+" host rekey added. Otherwise, do not connect.") {
		t.Errorf("another --host-key: exit %d\n%s", code, got)
	}
	// Another target that shows another key: the pin stays, and so does the
	// saved target, whatever the target's spelling.
	shown := hostShows(t, th.h.Home, "trusted")
	if err := os.WriteFile(filepath.Join(th.h.Home, "changed"), []byte(changedLog(shown)), 0o600); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(th.h.Home, "other.conf")
	if err := os.WriteFile(conf, []byte("# another target's config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, code = lineErr(t, "host", "add", "added", "box.other", "--yes", "--ssh-config", conf)
	if code != 5 || !strings.Contains(got, "added's host key changed: "+fp+" was pinned, and the host shows "+shown) ||
		!strings.Contains(got, "host rekey added") {
		t.Errorf("another target: exit %d\n%s", code, got)
	}
	if h, _ := client.FindHost("added"); h.SSH != "box" || pinnedAdded(t) != fp {
		t.Errorf("after another target: %+v, pin %s", h, pinnedAdded(t))
	}
	_ = os.Remove(filepath.Join(th.h.Home, "changed"))
	// host rm forgets the pin, and no other.
	hostShows(t, th.h.Home, "trusted")
	okSaid(t, "host", "add", "added2", "box", "--tier", "own", "--yes")
	okSaid(t, "host", "rm", "added")
	if client.HasPin("added") || !client.HasPin("added2") {
		t.Errorf("after host rm: pin of added %v, of added2 %v", client.HasPin("added"), client.HasPin("added2"))
	}
	// host doctor shows the pin.
	if out, err := run(t, "host", "doctor", "added2"); err != nil || !strings.Contains(out, "host key") || !strings.Contains(out, " pinned") {
		t.Errorf("host doctor: %v\n%s", err, out)
	}
}

// With input off, host add pins only a key that it can check: one that the
// user's settings trust, or --host-key. Otherwise it fails with exit code
// 4, before it logs in or reads the key, and names --host-key. A host add
// that fails leaves no pin.
func TestHostAddNeedsAKeyItCanCheck(t *testing.T) {
	th := keyHost(t)
	hostShows(t, th.h.Home, "unknown")
	got, code := lineErr(t, "host", "add", "added", "box")
	if code != 4 || !strings.Contains(got, "Error: host add needs the fingerprint of added's host key, or --yes to pin the key that it shows, and input is off.") ||
		!strings.Contains(got, "Try: "+brand.Name+" host add added box --host-key SHA256:<fingerprint>") {
		t.Fatalf("host add with input off: exit %d\n%s", code, got)
	}
	if files := pinFiles(t); len(files) != 0 {
		t.Errorf("a pin was written: %v", files)
	}
	for _, l := range sshLines(t, th) {
		if !strings.Contains(l, " -G -- box") && !strings.Contains(l, " LogLevel=DEBUG1 ") {
			t.Errorf("host add logged in, or read the key: %q", l)
		}
	}
}

// A host add that fails after the pin, here at the helper's question,
// leaves no pin.
func TestHostAddThatFailsLeavesNoPin(t *testing.T) {
	if _, ok := helpers.For("linux", "arm64"); !ok {
		t.Skip("this build has no helper for linux/arm64 (run just helpers)")
	}
	th := newTestHost(t)
	th.h.Script("ssh", promptingSSH) // a Linux host without the helper
	hostShows(t, th.h.Home, "trusted")
	if got, code := lineErr(t, "host", "add", "added", "box", "--tier", "own"); code != 4 || !strings.Contains(got, "needs a yes to install the helper") {
		t.Errorf("the helper's question: exit %d\n%s", code, got)
	}
	if files := pinFiles(t); len(files) != 0 {
		t.Errorf("a failed host add left %v", files)
	}
}

// --yes pins the key that the host shows, on first use, and says how to
// check it; --host-key pins it only when it matches.
func TestHostAddOnFirstUse(t *testing.T) {
	th := keyHost(t)
	fp := hostShows(t, th.h.Home, "unknown")
	_, wrong := hostKey(t)
	got, code := lineErr(t, "host", "add", "added", "box", "--tier", "own", "--host-key", wrong)
	if code != 5 || !strings.Contains(got, "added shows the host key "+fp+", not "+wrong+", which --host-key names") || len(pinFiles(t)) != 0 {
		t.Fatalf("a --host-key that the host does not show: exit %d\n%s", code, got)
	}
	got, code = lineErr(t, "host", "add", "added", "box", "--tier", "own", "--host-key", "MD5:12:34")
	if code != 2 || !strings.Contains(got, "is not a SHA256 fingerprint") {
		t.Errorf("a --host-key that is not one: exit %d\n%s", code, got)
	}
	out := okSaid(t, "host", "add", "added", "box", "--tier", "own", "--host-key", fp)
	if !strings.Contains(out, "  ✓ host key "+fp+" pinned, matched --host-key\n") {
		t.Errorf("--host-key said:\n%s", out)
	}
	okSaid(t, "host", "rm", "added")
	out = okSaid(t, "host", "add", "added", "box", "--tier", "own", "--yes")
	if !strings.Contains(out, "  ✓ host key "+fp+" pinned on first use. Check it on the machine: ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub\n") {
		t.Errorf("--yes said:\n%s", out)
	}
	// The connections that read the key offered no key and no password.
	for _, l := range sshLines(t, th) {
		if !strings.Contains(l, " PreferredAuthentications=none ") {
			continue
		}
		for _, o := range []string{"PubkeyAuthentication=no", "PasswordAuthentication=no", "KbdInteractiveAuthentication=no", "IdentityAgent=none", "ControlMaster=no", "ForwardAgent=no"} {
			if !strings.Contains(" "+l+" ", " -o "+o+" ") {
				t.Errorf("a connection that read the key lacks -o %s: %q", o, l)
			}
		}
	}
}

// A key that the user's known_hosts no longer names: host add stops with
// exit code 5, and says to check the key and pass it, as host rekey knows
// no such host yet.
func TestHostAddWithAStaleKnownHost(t *testing.T) {
	th := keyHost(t)
	fp := hostShows(t, th.h.Home, "changed")
	got, code := lineErr(t, "host", "add", "added", "box", "--tier", "own", "--yes")
	if code != 5 || !strings.Contains(got, "Error: added shows the host key "+fp+", and your known_hosts holds another key for it.") ||
		!strings.Contains(got, "Try: If you reinstalled added, check its key on the machine (ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub), and pass it with --host-key.") {
		t.Errorf("host add: exit %d\n%s", code, got)
	}
	if _, err := client.FindHost("added"); err == nil || len(pinFiles(t)) != 0 {
		t.Errorf("the host was saved, or pinned: %v", pinFiles(t))
	}
	if out := okSaid(t, "host", "add", "added", "box", "--tier", "own", "--host-key", fp); !strings.Contains(out, " pinned, matched --host-key") {
		t.Errorf("with --host-key:\n%s", out)
	}
}

// A changed key stops every command with exit code 5 and the message of
// spec 14.14, also when ~/.ssh/config says StrictHostKeyChecking no.
func TestChangedHostKeyExitsWith5(t *testing.T) {
	th := keyHost(t)
	hostShows(t, th.h.Home, "trusted")
	okSaid(t, "host", "add", "added", "box", "--tier", "own", "--yes")
	fp := pinnedAdded(t)
	if err := os.MkdirAll(filepath.Join(th.h.Home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(th.h.Home, ".ssh", "config"), []byte("Host *\n  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n  LogLevel QUIET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	shown := "SHA256:" + strings.Repeat("Z", 43)
	if err := os.WriteFile(filepath.Join(th.h.Home, "changed"), []byte(changedLog(shown)), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(th.h.Home, "ssh.log"))
	got, code := lineErr(t, "host", "test", "added")
	if code != 5 || !strings.Contains(got, "Error: added's host key changed: "+fp+" was pinned, and the host shows "+shown+".") ||
		!strings.Contains(got, "Try: If you reinstalled added: "+brand.Name+" host rekey added. Otherwise, do not connect.") {
		t.Fatalf("host test: exit %d\n%s", code, got)
	}
	for _, l := range sshLines(t, th) {
		if !strings.Contains(" "+l+" ", " -o StrictHostKeyChecking=yes ") || !strings.Contains(" "+l+" ", " -o LogLevel=ERROR ") {
			t.Errorf("ssh ran without the pin's options: %q", l)
		}
	}
	out, errOut, code := mainRun(t, "host", "test", "added", "--json")
	var e struct {
		Error struct {
			Code, Kind string
			Exit       int
		}
	}
	if code != 5 || out != "" || json.Unmarshal([]byte(errOut), &e) != nil || e.Error.Code != "host_key_changed" || e.Error.Kind != "refused" || e.Error.Exit != 5 {
		t.Errorf("host test --json: exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
}

// host rekey: the typed name, --confirm, and input off.
func TestHostRekey(t *testing.T) {
	th := keyHost(t)
	hostShows(t, th.h.Home, "trusted")
	okSaid(t, "host", "add", "added", "box", "--tier", "own", "--yes")
	old := pinnedAdded(t)
	pin := filepath.Join(client.ConfigDir(), "ssh", "known_hosts-"+client.HostFileKey("added"))
	oldPin, err := os.ReadFile(pin)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(th.h.Home, "ssh.log"))

	got, code := lineErr(t, "host", "rekey", "added")
	if code != 4 || !strings.Contains(got, "Error: host rekey needs the host's typed name to replace the pinned host key of added, and input is off.") ||
		!strings.Contains(got, "Try: "+brand.Name+" host rekey added --confirm added") {
		t.Errorf("rekey with input off: exit %d\n%s", code, got)
	}
	if _, err := os.Stat(filepath.Join(th.h.Home, "ssh.log")); err == nil {
		t.Error("rekey with input off connected")
	}
	if got, code := lineErr(t, "host", "rekey", "added", "--confirm", "other"); code != 2 || !strings.Contains(got, "--confirm names other, and the host is added") {
		t.Errorf("a --confirm of another name: exit %d\n%s", code, got)
	}
	if _, code := lineErr(t, "host", "rekey", "nope", "--confirm", "nope"); code != 3 {
		t.Errorf("rekey of no host: exit %d", code)
	}

	fp := hostShows(t, th.h.Home, "changed")
	out := okSaid(t, "host", "rekey", "added", "--confirm", "added")
	if !strings.Contains(out, "  Pinned now:  "+old+"\n  added shows: "+fp+"\n") || !strings.Contains(out, "  ✓ host key "+fp+" pinned for added.") {
		t.Errorf("rekey --confirm said:\n%s", out)
	}
	if pinnedAdded(t) != fp {
		t.Error("rekey did not replace the pin")
	}
	if out := okSaid(t, "host", "rekey", "added", "--confirm", "added"); !strings.Contains(out, "added shows its pinned host key, "+fp+", so nothing changed.") {
		t.Errorf("rekey of the same key said:\n%s", out)
	}

	// At a terminal: Enter keeps the pin, other text cancels, and the name
	// replaces it. Each answer starts from the old pin.
	for _, c := range []struct {
		name, in string
		replaced bool
	}{{"Enter", "\n", false}, {"other text", "nope\n", false}, {"the name", "added\n", true}} {
		t.Run(c.name, func(t *testing.T) {
			if err := os.WriteFile(pin, oldPin, 0o600); err != nil {
				t.Fatal(err)
			}
			next := hostShows(t, th.h.Home, "changed")
			var b bytes.Buffer
			err := rekeyHost(context.Background(), testPrompts(c.in, &b), "added", "", "")
			if c.replaced != (err == nil) || c.replaced != (pinnedAdded(t) == next) || (!c.replaced && pinnedAdded(t) != old) {
				t.Errorf("%v, pinned %s", err, pinnedAdded(t))
			}
			if !c.replaced && (!isCancel(err) || err.Error() != "Cancelled. Nothing changed.") {
				t.Errorf("%v, want a cancel", err)
			}
			if !strings.Contains(b.String(), "Type added to pin the new key, or press Enter to keep the old one: ") {
				t.Errorf("the question:\n%s", b.String())
			}
		})
	}

	// A file that names no host: rekey refuses it, with exit code 5, and
	// leaves it.
	if err := os.WriteFile(pin, []byte("# nothing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostShows(t, th.h.Home, "changed")
	if got, code := lineErr(t, "host", "rekey", "added", "--confirm", "added"); code != 5 || !strings.Contains(got, "names no host") {
		t.Errorf("a file that names no host: exit %d\n%s", code, got)
	}
	if b, _ := os.ReadFile(pin); string(b) != "# nothing\n" {
		t.Errorf("rekey replaced it: %q", b)
	}
	// A pin of added's that holds no key: rekey says why, and replaces it.
	if err := os.WriteFile(pin, []byte("# "+brand.Name+" pin of the host added\n# nothing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := okSaid(t, "host", "rekey", "added", "--confirm", "added"); !strings.Contains(out, "Pinned now:  the pin cannot be read: ") {
		t.Errorf("an unreadable pin:\n%s", out)
	}
}
