package tui

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestWithKeymap(t *testing.T) {
	km := map[string]any{
		"session: delete": []any{"x", "Delete"},
		"session: rename": "",
		"host: add":       false,
	}
	cmds := withKeymap(registry(), km)
	find := func(name string) *command {
		for _, c := range cmds {
			if c.name() == name {
				return c
			}
		}
		t.Fatalf("no command %q", name)
		return nil
	}
	if d := find("session: delete"); len(d.keys) != 2 || d.keys[0].key != "x" || d.keys[1].key != "delete" {
		t.Errorf("delete keys = %+v", d.keys)
	}
	if r := find("session: rename"); len(r.keys) != 0 {
		t.Errorf("rename should be unbound, keys = %+v", r.keys)
	}
	if a := find("host: add"); len(a.keys) != 0 {
		t.Errorf("host add should be unbound, keys = %+v", a.keys)
	}
	if n := find("session: new"); len(n.keys) == 0 {
		t.Error("an untouched command lost its keys")
	}
}

type keymapBackend struct {
	*fakeBackend
	km map[string]any
}

func (b keymapBackend) Settings() client.Settings {
	s := b.fakeBackend.Settings()
	s.Keymap = b.km
	return s
}

func (b keymapBackend) ReadSettings() (client.Settings, string) { return b.Settings(), "" }

func TestOldKeymapPreview(t *testing.T) {
	defer func() { commands = registry() }()

	// The old name still binds session: peek, unless session: peek is set too.
	cmds := withKeymap(registry(), map[string]any{"session: preview": "p"})
	var peek *command
	for _, c := range cmds {
		if c.name() == "session: peek" {
			peek = c
		}
	}
	if peek == nil || len(peek.keys) != 1 || peek.keys[0].key != "p" {
		t.Fatalf("session: preview did not bind session: peek: %+v", peek)
	}
	both := withKeymap(registry(), map[string]any{"session: preview": "p", "session: peek": "v"})
	for _, c := range both {
		if c.name() == "session: peek" && (len(c.keys) != 1 || c.keys[0].key != "v") {
			t.Fatalf("session: peek did not win over session: preview: %+v", c.keys)
		}
	}

	// The launcher shows the shared deprecation warning at start, including
	// both the removal version and the removal date (cli-spec 3.1).
	m := New(context.Background(), keymapBackend{newFakeBackend(), map[string]any{"session: preview": "p"}}, Options{Now: func() time.Time { return now }})
	m.Init()
	want := client.OldKeymapPreview.Warning()
	if len(m.toasts.shown) != 1 || m.toasts.shown[0].kind != toastWarning ||
		m.toasts.shown[0].text != want ||
		!strings.Contains(want, "until v0.4.0 and 11 November 2027, whichever comes later.") ||
		m.toasts.shown[0].hint != "Rename it in config.toml." {
		t.Errorf("startup toasts = %+v, want %q", m.toasts.shown, want)
	}

	// Changing the shared deprecation record's version and date changes the
	// launcher's startup notice without touching TUI code.
	origVersion := client.OldKeymapPreview.Version
	client.ReleaseDays["9.9.0"] = "2030-01-31"
	client.OldKeymapPreview.Version = "9.9.0"
	defer func() {
		client.OldKeymapPreview.Version = origVersion
		delete(client.ReleaseDays, "9.9.0")
	}()
	m2 := New(context.Background(), keymapBackend{newFakeBackend(), map[string]any{"session: preview": "p"}}, Options{Now: func() time.Time { return now }})
	m2.Init()
	if len(m2.toasts.shown) != 1 || !strings.HasSuffix(m2.toasts.shown[0].text, "until v9.11.0 and 31 January 2031, whichever comes later.") {
		t.Errorf("mutated deprecation notice = %+v", m2.toasts.shown)
	}

	clean := New(context.Background(), keymapBackend{newFakeBackend(), map[string]any{"session: peek": "p"}}, Options{Now: func() time.Time { return now }})
	clean.Init()
	if len(clean.toasts.shown) != 0 {
		t.Errorf("clean keymap showed toasts = %+v", clean.toasts.shown)
	}
}

func TestPaletteSaysPeekNeverPreview(t *testing.T) {
	for _, c := range registry() {
		if strings.Contains(strings.ToLower(c.name()), "preview") || strings.Contains(strings.ToLower(c.label()), "preview") {
			t.Errorf("%s (%q) still says preview", c.name(), c.label())
		}
	}
	m := New(context.Background(), newFakeBackend(), Options{Now: func() time.Time { return now }})
	p := &palette{in: newInput("", "")}
	p.paste("peek")
	items := p.items(m)
	if len(items) == 0 || items[0].c.name() != "session: peek" {
		t.Fatalf("typing peek in the palette matched %+v", items)
	}
	none := &palette{in: newInput("", "")}
	none.paste("preview")
	if got := none.items(m); len(got) != 0 {
		t.Errorf("typing preview in the palette matched %+v", got)
	}
}

func TestScreenLinesCleansAndRenders(t *testing.T) {
	// Zero-width joiners (\u200c, \u200d) and Unicode tags (\U000e0041)
	// survive the VT emulator as grapheme clusters and require api.CleanText.
	raw := []byte("\x1b[32mready\x1b[0m   \r\n" +
		"日本\u200c語\u200d!\U000e0041\x1b]52;c;aGk=\x07\u202ehidden   \r\n" +
		"\x1b[4;3Hcursor")
	lines := ScreenLines(raw, 20, 5)
	if len(lines) != 5 {
		t.Fatalf("len(lines) = %d, want 5", len(lines))
	}
	for _, bad := range []string{"\u200c", "\u200d", "\U000e0041", "\u202e", "\x1b", "\x07"} {
		if strings.Contains(strings.Join(lines, "\n"), bad) {
			t.Fatalf("ScreenLines kept %q in %q", bad, lines)
		}
	}
	want := []string{"ready", "日本語!hidden", "", "  cursor", ""}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestScreenLinesBoundsHostileCSIAndCancels(t *testing.T) {
	// Warm up one-time table initialisation in vt/ultraviolet before timing.
	_, _ = ScreenLinesContext(context.Background(), []byte("ok"), 20, 5)
	for _, tc := range []struct {
		name string
		in   []byte
		want string
	}{
		{"rep 2B", []byte("A\x1b[2000000000b"), strings.Repeat("A", 20)},
		{"rep overflow", []byte("A\x1b[99999999999999999999b"), strings.Repeat("A", 20)},
		{"rep C1", []byte("A\x9b2000000000b"), strings.Repeat("A", 20)},
		{"rep DEL split", []byte("A\x1b[2\x7f000000000b"), strings.Repeat("A", 20)},
		{"rep flood", []byte("A" + strings.Repeat("\x1b[2000000000b", 4500)), strings.Repeat("A", 20)},
		{"tab forward 2B", []byte("A\x1b[2000000000IB"), ""},
		{"tab back 2B", []byte("\x1b[1;10H\x1b[2000000000ZB"), "B"},
		{"scroll up 2B", []byte("gone\x1b[2000000000S\x1b[Hkept"), "kept"},
		{"insert chars 2B", []byte("tail\x1b[1G\x1b[2000000000@head"), "head"},
		{"erase chars 2B", []byte("secret\x1b[1G\x1b[2000000000Xok"), "ok"},
		{"rgb sgr intact", []byte("\x1b[?1049h\x1b[38;2;255;128;64mok\x1b[0m"), "ok"},
	} {
		t0 := time.Now()
		lines, err := ScreenLinesContext(context.Background(), tc.in, 20, 5)
		elapsed := time.Since(t0)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", tc.name, err)
		}
		if elapsed >= 50*time.Millisecond {
			t.Errorf("%s took %v, want < 50ms", tc.name, elapsed)
		}
		if tc.want != "" && lines[0] != tc.want {
			t.Errorf("%s: line 0 = %q, want %q", tc.name, lines[0], tc.want)
		}
	}

	// A pre-cancelled context aborts before touching the emulator.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ScreenLinesContext(cancelled, []byte("hello"), 20, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled ScreenLinesContext = %v, want context.Canceled", err)
	}

	// Mid-render cancellation stops between write chunks.
	midCtx, midCancel := context.WithCancel(context.Background())
	defer midCancel()
	chunks := 0
	previousChunk := onScreenChunk
	onScreenChunk = func() {
		chunks++
		midCancel()
	}
	defer func() { onScreenChunk = previousChunk }()
	big := bytes.Repeat([]byte("line of screen output\r\n"), 1200)
	if _, err := ScreenLinesContext(midCtx, big, 80, 24); !errors.Is(err, context.Canceled) || chunks != 1 {
		t.Fatalf("mid-render ScreenLinesContext = %v after %d chunks, want context.Canceled after 1 chunk", err, chunks)
	}
}

func TestLauncherReconnectRetiresMasterAndReportsStuckMaster(t *testing.T) {
	hs := home(t, 80, 24)
	oldConn := hs.be.conn("atlas-dev")
	var retiredAfterClose bool
	hs.be.retire = func(_ context.Context, h client.Host) error {
		retiredAfterClose = oldConn.isClosed()
		hs.be.record("retire %s", h.Name)
		return nil
	}

	hs.key("ctrl+o")
	hs.typeText("host reconnect")
	hs.key("enter")

	h := hs.m.hostByName("atlas-dev")
	if !retiredAfterClose {
		t.Fatal("reconnect retired the SSH master before closing the old bridge connection")
	}
	if !h.online() || !slices.Contains(hs.be.callList(), "retire atlas-dev") {
		t.Fatalf("palette reconnect failed: status=%v calls=%v", h.status, hs.be.callList())
	}

	// When the shared SSH master refuses to exit (even with a timeout message),
	// reconnect marks the host failed (not offline), shows a toast, and never
	// dials a new channel over the stuck master.
	dialsBefore := hs.be.dials["atlas-dev"]
	retireCalls := 0
	hs.be.retire = func(_ context.Context, host client.Host) error {
		retireCalls++
		return &client.UserError{
			Msg:  "could not close the shared SSH connection to " + host.Name + ": timed out",
			Hint: "stop the ssh master",
			Code: "reconnect_failed",
		}
	}
	hs.key("i", "r")
	if h.status != stFailed {
		t.Fatalf("stuck master status = %v, want stFailed (%v)", h.status, stFailed)
	}
	if hs.be.dials["atlas-dev"] != dialsBefore {
		t.Fatalf("stuck master still dialled %d times (was %d)", hs.be.dials["atlas-dev"], dialsBefore)
	}
	texts := toastTexts(hs.m)
	if !slices.Contains(texts, "Could not close the shared SSH connection to atlas-dev: timed out.") {
		t.Fatalf("missing reconnect failure toast in %q", texts)
	}

	// Background retry after a failed reconnect still attempts RetireMaster
	// before dialling, and succeeds once the stuck master clears.
	if n := hs.fire(func(m tea.Msg) bool { r, ok := m.(redialMsg); return ok && r.name == "atlas-dev" }); n != 1 {
		t.Fatalf("scheduled %d redials, want 1", n)
	}
	if retireCalls != 2 || hs.be.dials["atlas-dev"] != dialsBefore {
		t.Fatalf("redial bypassed RetireMaster: retireCalls=%d dials=%d", retireCalls, hs.be.dials["atlas-dev"])
	}

	hs.be.retire = func(_ context.Context, _ client.Host) error {
		retireCalls++
		return nil
	}
	hs.fire(func(m tea.Msg) bool { r, ok := m.(redialMsg); return ok && r.name == "atlas-dev" })
	if !h.online() || retireCalls != 3 || hs.be.dials["atlas-dev"] != dialsBefore+1 {
		t.Fatalf("recovery after stuck master cleared: online=%v retireCalls=%d dials=%d", h.online(), retireCalls, hs.be.dials["atlas-dev"])
	}

}

// CheckKey takes what the launcher can match, and says why it cannot
// match the rest.
func TestCheckKey(t *testing.T) {
	for _, k := range []string{"x", "?", "!", "ctrl+o", "Ctrl+O", "alt+e", "ctrl+shift+a", "ctrl+shift+up", "space", "enter", "Enter", "esc", "tab",
		"shift+tab", "shift+space", "backspace", "delete", "pgdown", "f1", "F12", "ctrl++", "+", "é", "ctrl+alt+shift+meta+hyper+super+a"} {
		if err := CheckKey(k); err != nil {
			t.Errorf("CheckKey(%q) = %v", k, err)
		}
	}
	for _, c := range []struct{ key, want string }{
		{"", `"" is neither one character`},
		{"escape", `"escape" is neither one character`},
		{"ctrl+", `"ctrl+" is neither one character`},
		{"ctrl+ab", `"ab" is neither one character`},
		{"opt+e", `"opt" is not a modifier`},
		{"ctrl+ctrl+a", `write "ctrl+a"`},
		{"shift+ctrl+a", `write "ctrl+shift+a"`},
		{"ctrl+ ", `" " is neither one character`},
		// Keys that parse, and that the launcher never matches.
		{"X", `reads "X" as "x"`},
		{"É", `reads "É" as "é"`},
		{"shift+a", `a capital letter arrives as "A"`},
		{"shift+1", "bind that character instead"},
		{"++", "nothing comes before"},
		{"+x", "nothing comes before"},
		{"+f1", "nothing comes before"},
	} {
		if err := CheckKey(c.key); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("CheckKey(%q) = %v, want an error with %q", c.key, err, c.want)
		}
	}
}

// Every default key of the launcher passes the check.
func TestDefaultKeysPass(t *testing.T) {
	for _, c := range registry() {
		for _, b := range c.keys {
			if err := CheckKey(b.key); err != nil {
				t.Errorf("%s: %v", c.name(), err)
			}
		}
	}
}
