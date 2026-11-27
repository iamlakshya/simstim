package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// The writers of lists never touch the session last attached to: it is in
// a file of its own. A launcher.json from before that file keeps its last
// session.
func TestListWritesKeepTheLastSession(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	RememberLast("box", "a1", "web")
	before, err := os.ReadFile(lastPath())
	if err != nil {
		t.Fatal(err)
	}
	RememberSessions(map[string][]Session{"box": {{ID: "b2", Name: "api"}}})
	// The launcher writes its lists with the last session it loaded, which
	// may be older than the file's.
	if err := saveSessionCache(SessionCache{Sessions: map[string][]Session{"nuc": nil}, Last: &LastSession{Host: "nuc", ID: "old"}}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(lastPath())
	if err != nil || string(after) != string(before) {
		t.Fatalf("a list write changed the last session: %s, then %s (%v)", before, after, err)
	}
	if last, ok := LoadLast(); !ok || last.Host != "box" || last.ID != "a1" {
		t.Errorf("LoadLast() = %+v, %v", last, ok)
	}

	// An older launcher.json has the last session in it.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.MkdirAll(StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	old := `{"sessions":{"box":[{"id":"a1","name":"web"}]},"last":{"host":"box","id":"a1","name":"web"}}`
	if err := os.WriteFile(filepath.Join(StateDir(), "launcher.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	RememberSessions(map[string][]Session{"nuc": {{ID: "c3"}}})
	if last, ok := LoadLast(); !ok || last.Host != "box" || last.ID != "a1" {
		t.Errorf("after a list write, the older file's last session is %+v, %v", last, ok)
	}
}

// cacheTestClock sets the cache's clock to start, and returns what moves it.
func cacheTestClock(t *testing.T, start time.Time) func(time.Duration) {
	t.Helper()
	now := start
	old := cacheNow
	cacheNow = func() time.Time { return now }
	t.Cleanup(func() { cacheNow = old })
	return func(d time.Duration) { now = now.Add(d) }
}

// A writer changes only its own hosts, and records when each list arrived
// (cli-spec 9.2).
func TestAWriterChangesOnlyItsHosts(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	start := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	advance := cacheTestClock(t, start)
	RememberSessions(map[string][]Session{"box": {{ID: "b1"}}, "nuc": {{ID: "n1"}}})
	advance(time.Minute)
	RememberSessions(map[string][]Session{"nuc": {{ID: "n2"}}})
	c := LoadSessionCache()
	if len(c.Sessions["box"]) != 1 || c.Sessions["box"][0].ID != "b1" || c.Sessions["nuc"][0].ID != "n2" {
		t.Errorf("the lists are %+v", c.Sessions)
	}
	if !c.Seen["box"].Equal(start) || !c.Seen["nuc"].Equal(start.Add(time.Minute)) {
		t.Errorf("the times are %v", c.Seen)
	}
}

// A list keeps the time when it arrived, as the launcher saves it later.
// A list older than the cache's changes nothing, unless the cache's time
// is in the future.
func TestAListKeepsItsTime(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	start := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	cacheTestClock(t, start)
	arrived := start.Add(-2 * time.Minute)
	RememberLists(map[string]HostList{"box": {Sessions: []Session{{ID: "old"}}, At: arrived}})
	if c := LoadSessionCache(); !c.Seen["box"].Equal(arrived) {
		t.Errorf("the time is %v, want the time the list arrived, %v", c.Seen["box"], arrived)
	}
	RememberSessions(map[string][]Session{"box": {{ID: "new"}}})
	RememberLists(map[string]HostList{"box": {Sessions: []Session{{ID: "older"}}, At: arrived}})
	if c := LoadSessionCache(); c.Sessions["box"][0].ID != "new" || !c.Seen["box"].Equal(start) {
		t.Errorf("an older list replaced a newer one: %+v at %v", c.Sessions["box"], c.Seen["box"])
	}
	RememberLists(map[string]HostList{"nuc": {Sessions: []Session{{ID: "n"}}, At: start.Add(time.Hour)}})
	RememberLists(map[string]HostList{"nuc": {Sessions: []Session{{ID: "m"}}, At: start}})
	if c := LoadSessionCache(); c.Sessions["nuc"][0].ID != "m" {
		t.Errorf("a time in the future kept its list: %+v", c.Sessions["nuc"])
	}
}

// new, rm and rename change one row, and keep the time of the list.
func TestOneRowChanges(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	start := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	cacheTestClock(t, start)
	RememberSessions(map[string][]Session{"box": {{ID: "a", Name: "one"}}})
	RememberSession("box", Session{ID: "b", Name: "two"})
	RememberSession("box", Session{ID: "a", Name: "renamed"})
	ForgetSession("box", "b")
	c := LoadSessionCache()
	if len(c.Sessions["box"]) != 1 || c.Sessions["box"][0].Name != "renamed" || !c.Seen["box"].Equal(start) {
		t.Errorf("the list is %+v at %v", c.Sessions["box"], c.Seen["box"])
	}
}

// A temporary file that a crash left goes after an hour. last.json is made
// through a temporary file too.
func TestStaleTemporaryFilesGo(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.MkdirAll(StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(StateDir(), "launcher.json.123.tmp")
	fresh := filepath.Join(StateDir(), "launcher.json.456.tmp")
	for _, f := range []string{left, fresh} {
		if err := os.WriteFile(f, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(left, old, old); err != nil {
		t.Fatal(err)
	}
	RememberSessions(map[string][]Session{"box": nil})
	if _, err := os.Stat(left); err == nil {
		t.Error("the stale temporary file stayed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a new temporary file went")
	}
	if err := makeOnce(lastPath(), []byte(`{"host":"box","id":"a1"}`)); err != nil {
		t.Fatal(err)
	}
	if err := makeOnce(lastPath(), []byte(`{"host":"nuc","id":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if last, ok := LoadLast(); !ok || last.Host != "box" {
		t.Errorf("makeOnce replaced last.json: %+v", last)
	}
}

// Two writers of the session cache at once leave a file that parses, and
// no temporary file behind (cli-spec 9.8).
func TestTwoWritersLeaveAFileThatParses(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var wg sync.WaitGroup
	for w := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				host := fmt.Sprintf("host-%d", w)
				RememberSessions(map[string][]Session{host: {{ID: fmt.Sprintf("%d-%d", w, i), Name: "s"}}})
				if i%10 == 0 {
					RememberLast(host, "x", "s")
				}
			}
		}()
	}
	wg.Wait()
	b, err := os.ReadFile(cachePath())
	if err != nil {
		t.Fatal(err)
	}
	var c SessionCache
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("the file does not parse: %v\n%s", err, b)
	}
	if _, ok := LoadLast(); !ok {
		t.Error("last.json does not parse")
	}
	left, _ := filepath.Glob(filepath.Join(StateDir(), "*.tmp"))
	if len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// The text that a deletion's question shows: what stops and what stays.
func TestDeleteLines(t *testing.T) {
	running := DeleteLines(Session{Name: "auth-fix", Agent: "claude", State: "working"}, "box", "~/src/app")
	if len(running) != 2 || running[0] != "This stops claude on box and removes the session." ||
		running[1] != "The folder ~/src/app stays. Unsaved work in the agent is lost." {
		t.Errorf("a running agent: %q", running)
	}
	ended := DeleteLines(Session{Name: "auth-fix", Agent: "claude", State: "exited"}, "box", "/srv/app")
	if len(ended) != 2 || ended[0] != "This removes the session from box." || ended[1] != "The folder /srv/app stays." {
		t.Errorf("an ended agent: %q", ended)
	}
	// A worktree that the session made goes when it is clean. The helper
	// does not say which session made it, so the line is true either way.
	wt := "/home/dev/.local/share/" + brand.Name + "/worktrees/app/fix-1a2b"
	lines := DeleteLines(Session{Agent: "claude", State: "working", Cwd: wt}, "box", wt)
	if lines[1] != "If the session made the worktree "+wt+", it goes with its branch, unless it has changes or new commits. Unsaved work in the agent is lost." {
		t.Errorf("a worktree: %q", lines)
	}
	// The host's words are cleaned.
	lines = DeleteLines(Session{Agent: "cl\x1bcaude\x07", State: "working"}, "box", "/srv/\u202eapp\x1b]52;c;aGk=\x07")
	if s := strings.Join(lines, ""); strings.ContainsAny(s, "\x1b\x07\u202e") {
		t.Errorf("the lines keep control characters: %q", lines)
	}
}

// A state folder whose path contains glob metacharacters such as "[ab]"
// cleans up stale temporary files in its own folder without touching a
// sibling folder such as "statea" (decision 11, addendum A3).
func TestCacheCleanupStaysInItsFolder(t *testing.T) {
	root := t.TempDir()
	stateRoot := filepath.Join(root, "state[ab]")
	siblingDir := filepath.Join(root, "statea", brand.Name)
	if err := os.MkdirAll(siblingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("LOCALAPPDATA", stateRoot)

	ownDir := StateDir()
	if err := os.MkdirAll(ownDir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * staleTemp)
	ownTmp := filepath.Join(ownDir, "launcher.json.123.tmp")
	siblingTmp := filepath.Join(siblingDir, "launcher.json.456.tmp")
	siblingLastTmp := filepath.Join(siblingDir, "last.json.789.tmp")
	for _, f := range []string{ownTmp, siblingTmp, siblingLastTmp} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f, old, old); err != nil {
			t.Fatal(err)
		}
	}

	RememberSessions(map[string][]Session{"box": {{ID: "s1", Name: "app"}}})

	if _, err := os.Stat(ownTmp); !os.IsNotExist(err) {
		t.Errorf("stale temp inside own state folder remained: %v", err)
	}
	for _, sib := range []string{siblingTmp, siblingLastTmp} {
		if _, err := os.Stat(sib); err != nil {
			t.Errorf("sibling statea temp file %s was touched: %v", sib, err)
		}
	}
}
