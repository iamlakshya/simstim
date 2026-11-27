package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestSetupTrustIsKeptByHostAndRepository(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := LoadSetupAnswers("box"); len(got.Refused)+len(got.Hashes)+len(got.Trusted) != 0 || got.Warning != nil {
		t.Fatalf("fresh: %+v", got)
	}
	if _, err := RememberSetup("box", "/home/me/src/app", true, "h-app"); err != nil {
		t.Fatal(err)
	}
	if _, err := RememberSetup("box", "/home/me/src/api", false, "h-api"); err != nil {
		t.Fatal(err)
	}
	if _, err := RememberSetup("other", "/home/me/src/app", false, "h-app"); err != nil {
		t.Fatal(err)
	}
	got := LoadSetupAnswers("box")
	if got.Hashes["/home/me/src/app"] != "h-app" || !got.Trusted["/home/me/src/app"] {
		t.Errorf("app: %+v", got)
	}
	if run, ok := got.Refused["/home/me/src/api"]; !ok || run || got.Trusted["/home/me/src/api"] {
		t.Errorf("api: %+v", got)
	}
	if o := LoadSetupAnswers("other"); o.Trusted["/home/me/src/app"] || o.Hashes["/home/me/src/app"] != "" {
		t.Errorf("an answer leaked to another host: %+v", o)
	}
	// A new session's arguments carry the refusals and the hashes only.
	var a NewArgs
	got.Apply(&a)
	if !maps.Equal(a.SetupTrust, map[string]bool{"/home/me/src/api": false}) || !maps.Equal(a.SetupHashes, map[string]string{"/home/me/src/app": "h-app"}) {
		t.Errorf("args: %v %v", a.SetupTrust, a.SetupHashes)
	}
	st, err := os.Stat(trustPath())
	// Windows has no Unix file modes to check.
	if err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Errorf("the answers file: %v %v", st, err)
	}
}

// v2 is the shape of version 2 of the file, to read in a test.
type v2 struct {
	Version int                                     `json:"version"`
	Hosts   map[string]map[string]bool              `json:"hosts"`
	Trusted map[string]map[string]map[string]string `json:"trusted"`
}

func readV2(t *testing.T) v2 {
	t.Helper()
	b, err := os.ReadFile(trustPath())
	if err != nil {
		t.Fatal(err)
	}
	var f v2
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return f
}

func writeTrust(t *testing.T, text string) {
	t.Helper()
	if err := os.MkdirAll(StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trustPath(), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The file's round trip: a yes keeps its hash and time, and removes the
// repository's no. A no removes its yes. Hosts keeps refusals only.
func TestSetupTrustFileVersion2(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := RememberSetup("box", "/r", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := RememberSetup("box", "/r", true, "h1"); err != nil {
		t.Fatal(err)
	}
	f := readV2(t)
	e := f.Trusted["box"]["/r"]
	if f.Version != 2 || len(f.Hosts["box"]) != 0 || e["sha256"] != "h1" || !strings.HasSuffix(e["at"], "Z") {
		t.Fatalf("after a yes: %+v", f)
	}
	if _, err := RememberSetup("box", "/r", false, "h1"); err != nil {
		t.Fatal(err)
	}
	if f := readV2(t); f.Hosts["box"]["/r"] || len(f.Trusted["box"]) != 0 {
		t.Fatalf("after a no: %+v", f)
	}
	if run, ok := LoadSetupAnswers("box").Refused["/r"]; !ok || run {
		t.Fatal("the no is not read back")
	}
}

// A file of version 1 reads as spec 19.2 says: a no stays, and a yes asks
// again once, since it has no hash. The client writes version 2 at its next
// answer.
func TestSetupTrustMigratesVersion1(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeTrust(t, `{"hosts": {"box": {"/yes": true, "/no": false}}}`)
	got := LoadSetupAnswers("box")
	if !got.Trusted["/yes"] || got.Hashes["/yes"] != "" {
		t.Errorf("a yes of version 1 must be trusted with no hash: %+v", got)
	}
	if run, ok := got.Refused["/no"]; !ok || run {
		t.Errorf("a no of version 1 must stay: %+v", got)
	}
	var a NewArgs
	got.Apply(&a)
	if len(a.SetupHashes) != 0 || a.SetupTrust["/yes"] {
		t.Errorf("a start must send no yes without a hash: %v %v", a.SetupTrust, a.SetupHashes)
	}
	if _, err := RememberSetup("box", "/yes", true, "h2"); err != nil {
		t.Fatal(err)
	}
	f := readV2(t)
	if f.Version != 2 || f.Trusted["box"]["/yes"]["sha256"] != "h2" || f.Hosts["box"]["/yes"] || !maps.Equal(f.Hosts["box"], map[string]bool{"/no": false}) {
		t.Fatalf("version 2: %+v", f)
	}
}

// An older client reads Hosts only, and writes the file back without
// Trusted. It still refuses what the user refused, and the new client then
// asks again for each yes: no path runs a script without a yes.
func TestSetupTrustRewriteByAnOlderClient(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := RememberSetup("box", "/a", true, "ha"); err != nil {
		t.Fatal(err)
	}
	if _, err := RememberSetup("box", "/b", false, ""); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(trustPath())
	if err != nil {
		t.Fatal(err)
	}
	// The older client: its file struct has Hosts alone.
	var old struct {
		Hosts map[string]map[string]bool `json:"hosts"`
	}
	if err := json.Unmarshal(b, &old); err != nil {
		t.Fatal(err)
	}
	if run, ok := old.Hosts["box"]["/b"]; !ok || run || old.Hosts["box"]["/a"] {
		t.Fatalf("the older client reads %v", old.Hosts)
	}
	old.Hosts["box"]["/c"] = true // its own yes
	b, _ = json.Marshal(old)
	writeTrust(t, string(b))

	got := LoadSetupAnswers("box")
	if len(got.Hashes) != 0 {
		t.Errorf("no yes may keep a hash after the rewrite: %v", got.Hashes)
	}
	if run, ok := got.Refused["/b"]; !ok || run {
		t.Errorf("the no must stay: %v", got.Refused)
	}
	if !got.Trusted["/c"] || got.Trusted["/a"] {
		t.Errorf("trusted: %v", got.Trusted)
	}
}

// A file that does not parse becomes setup-trust.json.bad, with a warning,
// and the client starts with no answers.
func TestSetupTrustFileThatDoesNotParse(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeTrust(t, `{"hosts": {"box": {"/r": fals`)
	got := LoadSetupAnswers("box")
	if got.Warning == nil || !strings.Contains(got.Warning.Msg, "could not read setup-trust.json, so each setup script asks again") ||
		!strings.Contains(got.Warning.Hint, "Compare the two files, then delete") {
		t.Fatalf("warning: %+v", got.Warning)
	}
	if len(got.Refused)+len(got.Trusted) != 0 {
		t.Errorf("answers from a bad file: %+v", got)
	}
	if b, err := os.ReadFile(trustPath() + ".bad"); err != nil || !strings.Contains(string(b), "fals") {
		t.Errorf("the bad file: %q %v", b, err)
	}
	if _, err := os.Stat(trustPath()); !os.IsNotExist(err) {
		t.Errorf("the bad file is still in place: %v", err)
	}
	// It warns once: the next read finds no file.
	if again := LoadSetupAnswers("box"); again.Warning != nil {
		t.Errorf("warned again: %+v", again.Warning)
	}
}

// A second file that does not parse never replaces the first .bad file,
// which the user was told to compare: it takes the next free name.
func TestSetupTrustKeepsAnOlderBadFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeTrust(t, "first bad file")
	LoadSetupAnswers("box")
	writeTrust(t, "second bad file")
	got := LoadSetupAnswers("box")
	if got.Warning == nil || !strings.Contains(got.Warning.Msg, "The old file is now setup-trust.json.bad.1.") ||
		got.Warning.Hint != "Compare the two files, then delete "+trustPath()+".bad.1." {
		t.Fatalf("warning: %+v", got.Warning)
	}
	for name, want := range map[string]string{".bad": "first bad file", ".bad.1": "second bad file"} {
		if b, err := os.ReadFile(trustPath() + name); err != nil || string(b) != want {
			t.Errorf("setup-trust.json%s = %q, %v; want %q", name, b, err, want)
		}
	}
}

// The names of the bad files have no limit: past .bad.100, the next free
// name is still found.
func TestSetupTrustBadFileNamesHaveNoLimit(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	writeTrust(t, "not json")
	for i := range 101 {
		name := trustPath() + ".bad"
		if i > 0 {
			name += "." + strconv.Itoa(i)
		}
		if err := os.WriteFile(name, []byte("older"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := LoadSetupAnswers("box")
	if got.Warning == nil || !strings.Contains(got.Warning.Msg, "The old file is now setup-trust.json.bad.101.") {
		t.Fatalf("warning: %+v", got.Warning)
	}
	if b, err := os.ReadFile(trustPath() + ".bad.101"); err != nil || string(b) != "not json" {
		t.Errorf("setup-trust.json.bad.101 = %q, %v", b, err)
	}
	if b, _ := os.ReadFile(trustPath() + ".bad.100"); string(b) != "older" {
		t.Errorf("an older bad file changed: %q", b)
	}
}

// A file that breaks while a question is open moves aside when the answer
// is kept. The answer starts a new file, and the warning that names the old
// one reaches the caller, apart from any failure to save.
func TestSetupTrustBreaksWhileAnswering(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := RememberSetup("box", "/old", false, ""); err != nil {
		t.Fatal(err)
	}
	if got := LoadSetupAnswers("box"); got.Warning != nil || len(got.Refused) != 1 {
		t.Fatalf("before: %+v", got)
	}
	writeTrust(t, `{"hosts": {"box": {"/old": fals`) // another program broke it
	warn, err := RememberSetup("box", "/r", true, "h1")
	if err != nil {
		t.Fatal(err)
	}
	if warn == nil || !strings.Contains(warn.Msg, "The old file is now setup-trust.json.bad.") || warn.Hint != "Compare the two files, then delete "+trustPath()+".bad." {
		t.Fatalf("warning: %+v", warn)
	}
	if b, _ := os.ReadFile(trustPath() + ".bad"); !strings.Contains(string(b), "fals") {
		t.Errorf("the broken file was not kept: %q", b)
	}
	if got := LoadSetupAnswers("box"); got.Warning != nil || got.Hashes["/r"] != "h1" || len(got.Refused) != 0 {
		t.Errorf("after: %+v", got)
	}
}

// saverEnv names the answers that a child process of
// TestSetupTrustSavesFromManyProcesses saves: "<process> <count>".
const saverEnv = "SETUP_TRUST_SAVER"

// TestSetupTrustSaverProcess is a child of
// TestSetupTrustSavesFromManyProcesses. It saves its answers, each with a
// read and a write of the file, as a client process does.
func TestSetupTrustSaverProcess(t *testing.T) {
	var p, n int
	if _, err := fmt.Sscan(os.Getenv(saverEnv), &p, &n); err != nil {
		t.Skip("a child of TestSetupTrustSavesFromManyProcesses")
	}
	for i := range n {
		repo, run := fmt.Sprintf("/p%d/r%d", p, i), i%2 == 0
		if _, err := RememberSetup("box", repo, run, "h-"+repo); err != nil {
			t.Fatal(err)
		}
	}
}

// Client processes that save answers at once lose none of them: each read,
// recovery and save holds the file's lock. Without it, two processes read
// the same file, and the later write drops the earlier answer.
func TestSetupTrustSavesFromManyProcesses(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	const procs, each = 4, 25
	cmds := make([]*exec.Cmd, procs)
	for p := range procs {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSetupTrustSaverProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), saverEnv+"="+strconv.Itoa(p)+" "+strconv.Itoa(each))
		cmds[p] = cmd
	}
	for _, cmd := range cmds {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for p, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("process %d: %v", p, err)
		}
	}
	readV2(t) // the file is valid JSON
	got := LoadSetupAnswers("box")
	if got.Warning != nil {
		t.Fatal(got.Warning)
	}
	for p := range procs {
		for i := range each {
			repo := fmt.Sprintf("/p%d/r%d", p, i)
			if i%2 == 0 && got.Hashes[repo] != "h-"+repo {
				t.Errorf("the yes for %s is lost", repo)
			}
			if run, ok := got.Refused[repo]; i%2 == 1 && (!ok || run) {
				t.Errorf("the no for %s is lost", repo)
			}
		}
	}
	if n := len(got.Hashes) + len(got.Refused); n != procs*each {
		t.Errorf("%d answers, want %d", n, procs*each)
	}
}

// While another client holds the file's lock past the wait, the check
// fails closed: a read trusts nothing, and an answer is not kept. Both say
// why, with a hint.
func TestSetupTrustLockHeldFailsClosed(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, err := RememberSetup("box", "/r", true, "h1"); err != nil {
		t.Fatal(err)
	}
	if _, err := RememberSetup("box", "/no", false, ""); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(trustPath())
	if err != nil {
		t.Fatal(err)
	}
	defer func(old time.Duration) { trustWait = old }(trustWait)
	trustWait = 50 * time.Millisecond
	// Another client: a lock on another handle of the same file.
	unlock, err := lockPath(trustPath()+".lock", time.Second, true)
	if err != nil {
		t.Fatal(err)
	}
	hint := "Another client is reading or saving setup answers. Wait a moment, then try again."
	got := LoadSetupAnswers("box")
	if len(got.Hashes)+len(got.Trusted)+len(got.Refused) != 0 {
		t.Errorf("a read under another's lock trusted %+v", got)
	}
	if w := got.Warning; w == nil || !strings.HasPrefix(w.Msg, "could not lock setup-trust.json, so no setup script is trusted this time: ") || w.Hint != hint {
		t.Errorf("warning: %+v", w)
	}
	warn, err := RememberSetup("box", "/r2", true, "h2")
	var ue *UserError
	if warn != nil || !errors.As(err, &ue) || !strings.HasPrefix(ue.Msg, "could not lock setup-trust.json, so the answer is not kept: ") || ue.Hint != hint {
		t.Errorf("RememberSetup = %v, %v; want the lock's error", warn, err)
	}
	if after, _ := os.ReadFile(trustPath()); string(after) != string(before) {
		t.Errorf("the file changed under another's lock:\n%s", after)
	}
	unlock()
	if got := LoadSetupAnswers("box"); got.Warning != nil || got.Hashes["/r"] != "h1" {
		t.Errorf("after the lock: %+v", got)
	}
}

// When a file that does not parse cannot move aside, an answer writes
// nothing over it, and says why.
func TestSetupTrustBadFileThatCannotMove(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	defer func(old func(string, string) error) { renameTrust = old }(renameTrust)
	renameTrust = func(string, string) error { return os.ErrPermission }
	writeTrust(t, "not json")
	got := LoadSetupAnswers("box")
	if got.Warning == nil || !strings.Contains(got.Warning.Msg, "could not move it aside") || got.Warning.Hint != "Fix or delete "+trustPath()+"." {
		t.Fatalf("warning: %+v", got.Warning)
	}
	warn, err := RememberSetup("box", "/r", true, "h")
	var ue *UserError
	if warn != nil || !errors.As(err, &ue) || !strings.Contains(ue.Msg, "no answer is kept") {
		t.Fatalf("RememberSetup = %v, %v; want the warning as the error", warn, err)
	}
	if b, _ := os.ReadFile(trustPath()); string(b) != "not json" {
		t.Errorf("the answer wrote over the file: %q", b)
	}
}

// The question's words follow what the user said before: a change only for
// a yes that kept another hash (spec 19.2).
func TestSetupPast(t *testing.T) {
	s := SetupAnswers{
		Trusted: map[string]bool{"/hashed": true, "/old": true},
		Hashes:  map[string]string{"/hashed": "h1"},
		Refused: map[string]bool{"/no": false},
	}
	for _, c := range []struct {
		repo, hash string
		want       SetupPast
	}{
		{"/new", "h1", SetupNotTrusted},
		{"/no", "h1", SetupNotTrusted},
		{"/hashed", "h2", SetupChanged},
		{"/hashed", "h1", SetupNotTrusted},
		{"/old", "h1", SetupUnchecked},
		{"/hashed", "", SetupNotTrusted}, // an older helper
		{"/old", "", SetupNotTrusted},
	} {
		if got := s.Past(SetupQuestion{Repo: c.repo, Hash: c.hash}); got != c.want {
			t.Errorf("%s with %q: %v, want %v", c.repo, c.hash, got, c.want)
		}
	}
	// An answer changes what comes next, as the file would.
	a := s.Answered(SetupQuestion{Repo: "/old", Hash: "h3"}, true)
	if a.Past(SetupQuestion{Repo: "/old", Hash: "h4"}) != SetupChanged || s.Hashes["/old"] != "" {
		t.Errorf("after a yes: %+v; before %+v", a, s)
	}
	if n := s.Answered(SetupQuestion{Repo: "/hashed", Hash: "h2"}, false); n.Trusted["/hashed"] || n.Hashes["/hashed"] != "" || len(n.Refused) != 2 {
		t.Errorf("after a no: %+v", n)
	}
}

func TestAnswerSetup(t *testing.T) {
	q := SetupQuestion{Repo: "/r", Script: "/r/.x/setup", Hash: "h"}
	start := NewArgs{SetupHashes: map[string]string{"/other": "o"}}
	a := AnswerSetup(start, q, true)
	if a.Setup != "" || !maps.Equal(a.SetupHashes, map[string]string{"/other": "o", "/r": "h"}) {
		t.Errorf("yes with a hash: %+v", a)
	}
	if len(start.SetupHashes) != 1 {
		t.Error("the answer changed the arguments it was given")
	}
	if a := AnswerSetup(NewArgs{}, q, false); a.Setup != api.SetupSkip || len(a.SetupHashes) != 0 {
		t.Errorf("no: %+v", a)
	}
	// An older helper sends no hash: the answer is for this start only.
	q.Hash = ""
	if a := AnswerSetup(NewArgs{}, q, true); a.Setup != api.SetupRun || len(a.SetupHashes) != 0 {
		t.Errorf("yes without a hash: %+v", a)
	}
}
