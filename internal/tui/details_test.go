package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
)

// The host details show each line of client.HostDetails, from the fixture
// that the test of `host show` uses too: the launcher and the command show
// the same data (docs/contributing/cli-spec.md 11.6).
func TestDetailsShowTheLinesOfHostShow(t *testing.T) {
	d := clienttest.HostDetails()
	be := newFakeBackend()
	be.hosts = []client.Host{d.Host}
	fh := &fakeHost{info: *d.Info}
	for i := range d.Sessions {
		fh.sessions = append(fh.sessions, client.Session{ID: fmt.Sprintf("s%d", i), Name: fmt.Sprintf("s%d", i), Agent: "shell",
			State: api.StateIdle, Alive: true, CreatedAt: ago(0)})
	}
	be.state[d.Host.Name] = fh
	hs := newHarness(t, be, 120, 36)
	hs.m.motion = false
	hs.key("i")
	screen := plainScreen(hs.m)
	lines := d.Lines()
	if len(lines) != 10 {
		t.Fatalf("the fixture gives %d lines, want one of each kind", len(lines))
	}
	for _, l := range lines {
		if want := fit(l.Label, labelW) + l.Text(); !strings.Contains(screen, want) {
			t.Errorf("the details lack %q:\n%s", want, screen)
		}
	}
}

// errClearRemoval is the warning of the fake's forget: the clean-up of
// the record. Tests set it.
var errClearRemoval error

// A record that the launcher's remove cannot clear is a warning: the host
// is gone from the file and from the screen alike.
func TestRemoveHostWhenTheCleanupFails(t *testing.T) {
	errClearRemoval = clearRemovalError(t, "atlas-dev")
	t.Cleanup(func() { errClearRemoval = nil })
	want, hint := errText(errClearRemoval)
	hs := home(t, 80, 24)
	hs.key("ctrl+o")
	hs.typeText("host remove")
	hs.key("enter", "enter")
	if hs.m.hostByName("atlas-dev") != nil || slices.ContainsFunc(hs.be.hosts, func(h client.Host) bool { return h.Name == "atlas-dev" }) {
		t.Fatal("the host is still on the screen or in the file")
	}
	texts := toastTexts(hs.m)
	if !slices.Contains(texts, "Forgot atlas-dev. Its sessions and the helper stay on the host.") || !slices.Contains(texts, sentence(want)) {
		t.Errorf("the toasts are %q", texts)
	}
	shows := false
	for _, ts := range append(append([]toast(nil), hs.m.toasts.shown...), hs.m.toasts.queue...) {
		shows = shows || ts.text == sentence(want) && ts.hint == hint && strings.HasPrefix(hint, "Delete ")
	}
	if !shows {
		t.Errorf("the warning has no next step: %q", hint)
	}
}

// clearRemovalError is the error that the real ClearRemoval returns for a
// record of host name that it cannot delete: a folder that is not empty
// stands where the record should be.
func clearRemovalError(t *testing.T, name string) error {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	r := client.LoadRemoval(client.Host{Name: name})
	if err := r.Record(client.RemovalDestroy); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(client.StateDir(), "hosts", name+"-*", "removal.json"))
	if len(files) != 1 {
		t.Fatalf("the record: %v", files)
	}
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(files[0], "in"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := client.ClearRemoval(name)
	if err == nil {
		t.Fatal("the record was cleared")
	}
	return err
}

// errText keeps the lines of an error, as an SSH error of a few lines of
// stderr has them, and cleans each one; a plain error too. An error is
// offline by its whole message, also past the part that the launcher
// draws.
func TestErrTextKeepsTheLines(t *testing.T) {
	ssh := &client.UserError{Msg: "cannot reach box: Warning: Permanently added 'box' (ED25519) to the list of known hosts.\r\n" +
		"ssh: connect to host box port 22: Connection refused", Hint: "ssh box"}
	msg, hint := errText(ssh)
	if msg != "cannot reach box: Warning: Permanently added 'box' (ED25519) to the list of known hosts.\nssh: connect to host box port 22: Connection refused" ||
		hint != "ssh box" {
		t.Errorf("errText = %q, %q", msg, hint)
	}
	if lines := wrap(msg, 70); len(lines) < 2 || !strings.HasPrefix(lines[len(lines)-1], "ssh: connect") {
		t.Errorf("wrap draws %q", lines)
	}
	if plain, _ := errText(errors.New("exit 1:\x1bc\x1b]52;c;aGk=\x07\u202eevil\nnext")); plain != "exit 1:c]52;c;aGk=evil\nnext" {
		t.Errorf("a plain error: %q", plain)
	}
	long := &client.UserError{Msg: "the helper on box did not start: " + strings.Repeat("x", 2*errTextMax) + " Connection refused"}
	if !offline(long) {
		t.Error("a long refused error is not offline")
	}
	if all, _ := errText(long); len([]rune(all)) > errTextMax {
		t.Errorf("errText drew %d runes", len([]rune(all)))
	}
}

// The details of a host whose connection failed draw its error cleaned:
// what the host wrote to stderr, such as a reset of the terminal or a
// write to the clipboard, never reaches the screen.
func TestDetailsOfAFailedConnectionAreClean(t *testing.T) {
	be := newFakeBackend()
	be.hosts = []client.Host{{Name: "box", SSH: "box"}}
	be.state["box"] = &fakeHost{dialErr: &client.UserError{
		Msg:  "the helper on box did not start: \x1bc\x1b]52;c;aGk=\x07\x1bP+q\x1b\\\u202eevil" + strings.Repeat("x", 2000),
		Hint: "ssh box\x1b[2J",
	}}
	hs := newHarness(t, be, 120, 36)
	hs.m.motion = false
	hs.key("i")
	raw := hs.m.render()
	for _, bad := range []string{"\x1bc", "\x1b]52", "\x1bP", "\x07", "\u202e", "\x1b[2J"} {
		if strings.Contains(raw, bad) {
			t.Errorf("the details drew %q: %q", bad, raw)
		}
	}
	if !strings.Contains(plainScreen(hs.m), "The helper on box did not start: c]52;c;aGk=P+q\\evil") {
		t.Errorf("the details lack the cleaned error:\n%s", plainScreen(hs.m))
	}
	if msg, hint := errText(be.state["box"].dialErr); len([]rune(msg)) > errTextMax || strings.ContainsAny(msg+hint, "\x1b\x07\u202e") {
		t.Errorf("errText = %q, %q", msg, hint)
	}
}

// The launcher's remove keeps no record of an unfinished `host rm` of the
// host (cli-spec 11.3, rule 10).
func TestRemoveHostClearsTheRecord(t *testing.T) {
	hs := home(t, 80, 24)
	hs.key("ctrl+o")
	hs.typeText("host remove")
	hs.key("enter", "enter")
	if hs.m.hostByName("atlas-dev") != nil || !slices.Contains(hs.be.callList(), "clear removal atlas-dev") {
		t.Errorf("the record was not cleared: %v", hs.be.callList())
	}
}

// The host details never draw a host's text as it is: a reset of the
// terminal (ESC c), a write to the clipboard (OSC 52) or a bidirectional
// control in what the host sent stays out of the screen.
func TestDetailsAreClean(t *testing.T) {
	d := clienttest.HostDetails()
	evil := "\x1bc\x1b]52;c;aGk=\x07\u202e"
	d.Info.Distro += evil
	d.Info.AgentVersions["claude"] += evil
	be := newFakeBackend()
	be.hosts = []client.Host{d.Host}
	be.state[d.Host.Name] = &fakeHost{info: *d.Info}
	hs := newHarness(t, be, 120, 36)
	hs.m.motion = false
	hs.key("i")
	raw := hs.m.render()
	if strings.Contains(raw, "\x1bc") || strings.Contains(raw, "\x1b]52") || strings.ContainsAny(raw, "\x07\u202e") {
		t.Errorf("the details drew a host's escape sequence: %q", raw)
	}
	if !strings.Contains(plainScreen(hs.m), "Debian 13c]52;c;aGk=") {
		t.Errorf("the details lack the cleaned system:\n%s", plainScreen(hs.m))
	}
}
