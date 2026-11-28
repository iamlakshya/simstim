package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/tui"
)

// The last acceptance line of C4: `attach` with no argument opens the
// picker at a terminal, and fails with exit code 2 with input off.
func TestAttachPicksASession(t *testing.T) {
	newTestHost(t)
	newSession(t, "first", "/bin/sh", "-c", "exec sleep 60")
	newSession(t, "second", "/bin/sh", "-c", "echo PICKED-$((6*7)); exec sleep 60")

	term, done := inTerminal(t, "attach")
	term.WaitFor("box/second")
	term.Type("sec")
	term.WaitFor("1 of 2")
	term.Type("\r")
	term.WaitFor("PICKED-42")
	term.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	term.WaitFor("Detached. second keeps running on box.")

	// esc cancels, with exit code 130.
	term, done = inTerminal(t, "attach")
	term.WaitFor("box/first")
	term.Type("\x1b")
	err := wait(t, done)
	if !isCancel(err) {
		t.Fatalf("esc: %v", err)
	}
	// The last frame is written before the picker returns, and the
	// terminal's reader may show it a moment later.
	var screen string
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		lines, _ := hosttest.Screen(term.Output(), 90, 30)
		screen = strings.Join(lines, "\n")
		if !strings.Contains(screen, "box/first") || time.Now().After(deadline) {
			break
		}
	}
	if strings.Contains(screen, "box/first") {
		t.Errorf("the picker left its rows:\n%s", screen)
	}

	// With input off, the missing address is a usage error.
	got, code := lineErr(t, "attach", "--no-input")
	if code != 2 || !strings.Contains(got, "attach needs a session, as <host>/<session>") || !strings.Contains(got, "Example: "+brand.Name+" attach my-vps/auth-fix") {
		t.Errorf("attach --no-input: exit %d\n%s", code, got)
	}
}

// The picker starts from the session cache, and a host that does not
// answer keeps its cached sessions.
func TestPickerKeepsTheCacheOfAHostThatDoesNotAnswer(t *testing.T) {
	th := newTestHost(t)
	cache := `{"sessions":{"box":[{"id":"zzz","name":"cached-one","agent":"claude","state":"idle","created_at":"2026-11-10T10:00:00Z","alive":true}]}}`
	if err := os.MkdirAll(client.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client.StateDir(), "launcher.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
	th.tr.SetDown(errors.New("the host is down"))
	term, done := inTerminal(t, "attach")
	term.WaitFor("box/cached-one")
	screen := func() string {
		lines, _ := hosttest.Screen(term.Output(), 90, 30)
		return strings.Join(lines, "\n")
	}
	hosttest.WaitFor(t, 10*time.Second, "the hosts' answers", func() bool { return !strings.Contains(screen(), "checking hosts") })
	if s := screen(); !strings.Contains(s, "box/cached-one") || !strings.Contains(s, "1 of 1") {
		t.Errorf("the cached session went away:\n%s", s)
	}
	term.Type("\x1b")
	if err := wait(t, done); !isCancel(err) {
		t.Fatalf("esc: %v", err)
	}
}

// writeCache writes the session cache.
func writeCache(t *testing.T, cache string) {
	t.Helper()
	if err := os.MkdirAll(client.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(client.StateDir(), "launcher.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
}

const cachedOne = `{"sessions":{"box":[{"id":"zzz","name":"cached-one","agent":"claude","state":"idle","created_at":"2026-11-10T10:00:00Z","alive":true}]}}`

// pickErr runs attach at a terminal, waits for it, and returns its error as
// printError prints it, with its exit code.
func pickErr(t *testing.T) (string, int) {
	t.Helper()
	_, done := inTerminal(t, "attach")
	err := wait(t, done)
	var b strings.Builder
	code := printError(&b, err, nil, false, false)
	return b.String(), code
}

// With hosts that answer with no session, the picker closes with "no
// sessions on any host", and exit code 3. TestPickDrawsAtOnce, in the
// launcher's package, shows that it draws at once with an empty cache.
func TestAttachWithNoSessions(t *testing.T) {
	newTestHost(t)
	got, code := pickErr(t)
	if code != 3 || !strings.Contains(got, "no sessions on any host") || !strings.Contains(got, "Try: "+brand.Name+" new box") {
		t.Errorf("exit %d:\n%s", code, got)
	}

	// A refresh that empties a cached list closes the picker the same way.
	writeCache(t, cachedOne)
	if got, code = pickErr(t); code != 3 || !strings.Contains(got, "no sessions on any host") {
		t.Errorf("an emptied cache: exit %d:\n%s", code, got)
	}
}

// When no host answers and nothing is cached, attach fails with the
// host's reach error and exit code 1, not 3.
func TestAttachWhenNoHostAnswers(t *testing.T) {
	th := newTestHost(t)
	th.tr.SetDown(errors.New("the host is down"))
	got, code := pickErr(t)
	if code != 1 || strings.Contains(got, "no sessions on any host") || !strings.Contains(got, "Error: the host is down") {
		t.Errorf("exit %d:\n%s", code, got)
	}
}

// ls and the picker write what the hosts answered to the session cache, so
// that a user of the command line alone gets a cache too.
func TestListsWriteTheSessionCache(t *testing.T) {
	newTestHost(t)
	newSession(t, "kept", "sleep", "60")
	writeCache(t, `{}`) // new wrote its session there
	if rows := tui.CachedSessions([]string{"box"}); len(rows) != 0 {
		t.Fatalf("the cache has %d rows before any list", len(rows))
	}
	ok(t, "ls")
	if rows := tui.CachedSessions([]string{"box"}); len(rows) != 1 || rows[0].Session.Name != "kept" {
		t.Fatalf("after ls the cache has %+v", rows)
	}
	b, _ := os.ReadFile(filepath.Join(client.StateDir(), "launcher.json"))
	if !strings.Contains(string(b), `"kept"`) {
		t.Errorf("launcher.json = %s", b)
	}
	// The picker's refresh writes it too.
	writeCache(t, `{}`)
	hosts, _ := client.LoadHosts()
	rows, err := pickRefresh(hosts, nil)(context.Background())
	if err != nil || len(rows) != 1 || len(tui.CachedSessions([]string{"box"})) != 1 {
		t.Errorf("the refresh gave %d rows, %v, and cached %d", len(rows), err, len(tui.CachedSessions([]string{"box"})))
	}
}

// The picker's refresh never wakes a host: a provider script's start never
// runs, and a stopped machine stays stopped.
func TestPickerRefreshNeverWakes(t *testing.T) {
	h := hosttest.New(t)
	h.Script("ssh", "echo \"ssh: connect to host cloud port 22: Connection refused\" >&2; exit 255\n")
	t.Setenv("PATH", filepath.Join(h.Root, "bin")+":"+os.Getenv("PATH"))
	started := filepath.Join(h.Home, "started")
	script := h.Script("provider", "[ \"$1\" = start ] && touch "+started+"; true\n")
	m, _ := withFake(t, nil)
	m.SetState(client.MachineStopped)
	hosts := []client.Host{
		{Name: "cloud", SSH: "cloud", Provider: &client.Provider{Script: script, WakeOnAttach: true}},
		{Name: "vm", Machine: &client.Machine{Provider: "fake", ID: "fake-vm"}},
	}
	if err := client.SaveHosts(context.Background(), hosts); err != nil {
		t.Fatal(err)
	}
	cached := []tui.PickRow{{Host: "cloud", Session: client.Session{ID: "cc", Name: "c"}}}
	rows, err := pickRefresh(hosts, cached)(context.Background())
	if err == nil || len(rows) != 1 {
		t.Errorf("the refresh gave %+v, %v", rows, err)
	}
	if _, err := os.Stat(started); err == nil {
		t.Error("the refresh ran the provider script's start")
	}
	if strings.Contains(strings.Join(m.Calls(), " "), "start") {
		t.Errorf("the refresh started the machine: %v", m.Calls())
	}
}
