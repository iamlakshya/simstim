package client

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/term"
)

// notified runs notifyOthers over each list in turn and returns what it
// wrote to the terminal.
func notified(t *testing.T, lists ...[]Session) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a := &attacher{o: AttachOptions{Host: Host{Name: "box"}, Session: "mine", Out: f}}
	for _, l := range lists {
		a.notifyOthers(l)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNotifyOthers(t *testing.T) {
	first := []Session{
		{ID: "mine", Name: "this one", State: "working"},
		{ID: "a", Name: "api", State: "working"},
		{ID: "b", Name: "build", State: "working"},
		{ID: "c", Name: "chat", State: "idle"},
	}
	// The first list only records the states.
	if got := notified(t, first); got != "" {
		t.Fatalf("the first list notified: %q", got)
	}
	next := []Session{
		{ID: "mine", Name: "this one", State: "needs_input"}, // the attached one: the user sees it
		{ID: "a", Name: "api", State: "needs_input", Reason: "Allow; rm -rf?\x07"},
		{ID: "b", Name: "build", State: "errored"},
		{ID: "c", Name: "chat", State: "working"},    // not worth a notification
		{ID: "d", Name: "new", State: "needs_input"}, // unknown before
	}
	got := notified(t, first, next)
	title := brand.DisplayName + " · box"
	want := "\x1b]777;notify;" + title + ";api needs you: Allow  rm -rf?\x1b\\" +
		"\x1b]777;notify;" + title + ";build ended\x1b\\"
	if got != want {
		t.Errorf("notifications = %q, want %q", got, want)
	}
	if strings.Contains(got, "this one") {
		t.Error("the attached session was notified")
	}
}

// TestNotifyCleansHostText checks that a notification carries no control,
// bidirectional, zero-width or tag character that a host sent, so a session
// cannot end the notification early and write to the clipboard.
func TestNotifyCleansHostText(t *testing.T) {
	first := []Session{{ID: "a", Name: "api", State: "working"}}
	next := []Session{{ID: "a", Name: "api\u202egnp.exe", State: "needs_input",
		Reason: "ok\x1b\\\x1b]52;c;aGk=\x07\u009d52;c;aGk=\u009c\u200b\U000e0041"}}
	got := notified(t, first, next)
	title := brand.DisplayName + " · box"
	want := "\x1b]777;notify;" + title + ";apignp.exe needs you: ok\\]52 c aGk=52 c aGk=\x1b\\"
	if got != want {
		t.Errorf("notification = %q, want %q", got, want)
	}
}

// Notifications arriving mid-escape-sequence or mid-UTF-8 rune are deferred
// until the output stream returns to ground state, keeping every PTY output
// byte intact so the rendered terminal screen matches an uninterrupted stream
// (decision 11, addendum A2).
func TestNotifyDefersUntilGroundBoundary(t *testing.T) {
	render := func(raw []byte) string {
		emu := vt.NewEmulator(40, 6)
		_, _ = emu.Write(raw)
		return emu.String()
	}

	cases := []struct {
		name  string
		part1 []byte
		part2 []byte
	}{
		{
			name:  "split CSI clear screen",
			part1: []byte("stale\x1b["),
			part2: []byte("2J\x1b[HOK"),
		},
		{
			name:  "split 4-byte UTF-8 rune",
			part1: []byte("music: \xf0\x9d"),
			part2: []byte("\x84\x9e!"),
		},
		{
			name:  "split OSC title BEL",
			part1: []byte("\x1b]0;agent "),
			part2: []byte("title\x07OK"),
		},
		{
			name:  "split OSC ST escape",
			part1: []byte("\x1b]2;title\x1b"),
			part2: []byte("\\OK"),
		},
		{
			name:  "long OSC clipboard payload",
			part1: []byte("\x1b]52;c;" + strings.Repeat("A", 128<<10)),
			part2: []byte("RAWBASE64TAIL==\x07OK"),
		},
		{
			name:  "OSC aborted by CSI then split CSI with BEL",
			part1: []byte("stale\x1b]2;title\x1b[0m\x1b[\x07"),
			part2: []byte("2J\x1b[HOK"),
		},
		{
			name:  "high bytes preserve escape after OSC abort",
			part1: []byte("stale\x1b]2;title\x1b\xc3\xa9["),
			part2: []byte("2J\x1b[HOK"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "out")
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()

			a := &attacher{
				o:       AttachOptions{Host: Host{Name: "box"}, Session: "mine", Out: f},
				tracker: term.NewTracker(),
			}
			a.notifyOthers([]Session{{ID: "b", Name: "build", State: "working"}})

			a.output(0, tc.part1)
			a.notifyOthers([]Session{{ID: "b", Name: "build", State: "exited"}})
			if mid, err := os.ReadFile(f.Name()); err != nil {
				t.Fatal(err)
			} else if strings.Contains(string(mid), "777;notify") {
				t.Fatalf("notification emitted mid-sequence (%d bytes)", len(mid))
			}

			a.output(int64(len(tc.part1)), tc.part2)
			gotBytes, err := os.ReadFile(f.Name())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(gotBytes), "\x1b]777;notify;") {
				t.Fatalf("deferred notification was never flushed: %q", gotBytes)
			}
			wantBytes := append(append([]byte(nil), tc.part1...), tc.part2...)
			notice := []byte("\x1b]777;notify;" + brand.DisplayName + " · box;build ended\x1b\\")
			if got := bytes.ReplaceAll(gotBytes, notice, nil); !bytes.Equal(got, wantBytes) {
				t.Fatal("notification insertion changed the session's output bytes")
			}
			if gotScreen, wantScreen := render(gotBytes), render(wantBytes); gotScreen != wantScreen {
				t.Fatalf("screen mismatch:\ngot:\n%q\nwant:\n%q\nraw suffix: %q", gotScreen, wantScreen, gotBytes[max(0, len(gotBytes)-256):])
			}
		})
	}
}

// Notification metadata strips C1 and bidi controls without altering live PTY
// output bytes (decision 11, addendum A2).
func TestNotifyCleansC1AndBidiMetadata(t *testing.T) {
	first := []Session{{ID: "b", Name: "build\u009c\u009b2J\u202e", State: "working"}}
	next := []Session{{ID: "b", Name: "build\u009c\u009b2J\u202e", State: "needs_input", Reason: "review\u202e\u0085"}}
	got := notified(t, first, next)
	for _, bad := range []string{"\u009c", "\u009b", "\u0085", "\u202e"} {
		if strings.Contains(got, bad) {
			t.Errorf("notification retained %q in %q", bad, got)
		}
	}
	title := brand.DisplayName + " · box"
	want := "\x1b]777;notify;" + title + ";build2J needs you: review\x1b\\"
	if got != want {
		t.Errorf("cleaned notification = %q, want %q", got, want)
	}
}

func TestReconnectControlsRestoreNotificationBoundary(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a := &attacher{
		o: AttachOptions{Out: f}, tracker: term.NewTracker(),
		key: "box/unsafe\x1b]52;c;YQ\x07\u009c\u202e",
	}
	a.outStream.feed([]byte("\x1b]2;unfinished"))
	finish := a.reconnectNotice()
	if !a.outStream.ground() {
		t.Fatal("reconnect controls left the old OSC active")
	}
	a.emitNotify([]byte("NOTICE-START"))
	a.outStream.feed([]byte("\x1b["))
	finish()
	if !a.outStream.ground() {
		t.Fatal("restoring the reconnect title left the old CSI active")
	}
	a.outStream.feed([]byte("\x1b]2;unfinished"))
	a.clearFlash()
	a.emitNotify([]byte("NOTICE-REPLAY"))
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"\x1b]52", "\x07", "\u009c", "\u202e"} {
		if strings.Contains(string(data), bad) {
			t.Errorf("reconnect metadata retained %q", bad)
		}
	}
	for _, notice := range []string{"NOTICE-START", "NOTICE-REPLAY"} {
		if !bytes.Contains(data, []byte(notice)) {
			t.Errorf("reconnect lost %s", notice)
		}
	}
}

func TestRestoredHostTitleDoesNotClaimSafeGround(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	a := &attacher{o: AttachOptions{Host: Host{Name: "box"}, Out: f}, tracker: term.NewTracker()}
	// The old title parser could keep this as an OSC token, although the
	// ESC P aborts that OSC and leaves an unfinished DCS on the terminal.
	a.tracker.M.Title = []byte("\x1b]2;title\x1bPqpayload\x07")
	finish := a.reconnectNotice()
	finish()
	if a.outStream.ground() {
		t.Fatal("restored host bytes were assumed to be a complete control")
	}
	a.notify("NOTICE")
	before, err := os.ReadFile(f.Name())
	if err != nil || bytes.Contains(before, []byte("777;notify")) {
		t.Fatalf("notification split restored host title: %v", err)
	}
	a.writeOutput([]byte("\x1b\\OK"))
	after, err := os.ReadFile(f.Name())
	if err != nil || !bytes.Contains(after, []byte("777;notify")) {
		t.Fatalf("notification did not follow completed restored title: %v", err)
	}
}
