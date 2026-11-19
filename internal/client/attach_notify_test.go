package client

import (
	"os"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
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
