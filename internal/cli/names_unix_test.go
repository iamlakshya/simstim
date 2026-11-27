//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// liveList is the test host's sessions, as it lists them now.
func liveList(t *testing.T, th *testHost) []client.Session {
	t.Helper()
	var list []client.Session
	otherClient(t, th, func(ctx context.Context, conn *client.Conn) (err error) {
		list, err = conn.Sessions(ctx)
		return err
	})
	return list
}

// A session whose older name is another session's ID: the ID reaches the
// other session alone, with and without the host. Once that session is
// gone, its ID is not found, and the session named like it is not touched.
func TestAnIDIsNeverAName(t *testing.T) {
	th := newTestHost(t)
	x := newSession(t, "x", "sleep", "60")
	y := newSession(t, "y", "sleep", "60")
	hosttest.OldName(t, y, x)
	for _, word := range []string{"box/" + x, x} {
		if out := ok(t, "show", word); !strings.HasPrefix(out, "x on box\n") {
			t.Errorf("show %s:\n%s", word, out)
		}
	}
	if out := okSaid(t, "rm", "box/"+x, "-y"); !strings.HasPrefix(out, "Deleted x ("+x+") on box.") {
		t.Fatalf("rm box/%s -y: %q", x, out)
	}
	for _, args := range [][]string{{"rm", "box/" + x, "-y"}, {"rm", x, "-y"}, {"show", "box/" + x}, {"show", x}, {"resume", x, "--detach"}} {
		text, code := failsAs(t, args...)
		where := "on box"
		if !strings.Contains(args[1], "/") {
			where = "on any host"
		}
		if code != 3 || !strings.Contains(text, `no session has the ID "`+x+`" `+where) || !strings.Contains(text, `"box/`+y+`" (`+x+`)`) {
			t.Errorf("%s: exit %d\n%s", strings.Join(args, " "), code, text)
		}
	}
	if got := idOf(t, th, x); got != y {
		t.Errorf("the session named like the ID that went: %q, want %s", got, y)
	}
}

// new and rename refuse a name of the form of an ID, and a name that is
// too long, with exit code 2 and bad_name, before they open a connection.
// They make and change nothing.
func TestNewAndRenameRefuseAnIDsForm(t *testing.T) {
	th := newTestHost(t)
	long := strings.Repeat("n", api.SessionNameMax+1)
	for _, name := range []string{"abcd2345", long} {
		text, code := failsAs(t, "new", "box", "--detach", "--name", name)
		if code != 2 || !strings.Contains(text, "Try: ") || bridges(th.tr) != 0 {
			t.Errorf("new --name %.10s: exit %d, %d connections\n%s", name, code, bridges(th.tr), text)
		}
	}
	if text, _ := failsAs(t, "new", "box", "--detach", "--name", "abcd2345"); !strings.Contains(text, "looks like a session ID") ||
		!strings.Contains(text, "Try: Add a dash, as in abcd-2345, or use a name of another length.") {
		t.Errorf("the error of a name of an ID's form:\n%s", text)
	}
	_, errOut, code := mainRun(t, "new", "box", "--detach", "--json", "--name", "abcd2345")
	var line struct{ Error errorJSON }
	if code != 2 || json.Unmarshal([]byte(errOut), &line) != nil || line.Error.Code != "bad_name" {
		t.Errorf("new --json: exit %d, %q", code, errOut)
	}
	if list := liveList(t, th); len(list) != 0 {
		t.Fatalf("a refused new made %+v", list)
	}
	id := newSession(t, "a", "sleep", "60")
	for _, name := range []string{"abcd2345", long} {
		before := bridges(th.tr)
		if _, code := failsAs(t, "rename", "box/"+id, name); code != 2 || bridges(th.tr) != before {
			t.Errorf("rename to %.10s: exit %d, %d connections", name, code, bridges(th.tr)-before)
		}
	}
	if got := idOf(t, th, "a"); got != id {
		t.Errorf("after the refused renames, a is %q", got)
	}
	ok(t, "rename", "box/"+id, "abcd-2345")
}

// A name of 8 letters, such as frontend, is a name: new takes it, and an
// address without the host reaches the session by it, at a terminal too.
// A name of 8 characters with a digit is refused. A folder whose name
// looks like an ID gives a name that does not.
func TestEightLetterNamesAreNames(t *testing.T) {
	newTestHost(t)
	t.Setenv(noInputEnv, "")
	id := newSession(t, "frontend", "/bin/sh", "-c", "echo FRONT-$((6*7)); exec sleep 60")
	if out := ok(t, "show", "frontend"); !strings.Contains(out, "  ID              "+id+"\n") {
		t.Errorf("show frontend:\n%s", out)
	}
	term, done := inTerminal(t, "attach", "frontend")
	term.WaitFor("FRONT-42")
	term.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if _, code := failsAs(t, "new", "box", "--detach", "--name", "proj2345"); code != 2 {
		t.Errorf("new --name proj2345: exit %d", code)
	}
	var obj struct{ Name string }
	if err := json.Unmarshal([]byte(okJSON(t, "new", "box", "--detach", "--json", "--dir", "~/proj2345", "--mkdir", "--", "sleep", "60")), &obj); err != nil ||
		obj.Name != "proj2345-s" {
		t.Errorf("a session in ~/proj2345: %+v, %v", obj, err)
	}
}

// The address of a script, host/ID from --json, reaches its session, also
// when another session's older name is that ID.
func TestAScriptsAddressReachesItsSession(t *testing.T) {
	th := newTestHost(t)
	var obj struct{ ID, Address string }
	if err := json.Unmarshal([]byte(okJSON(t, "new", "box", "--detach", "--json", "--name", "a", "--", "sleep", "60")), &obj); err != nil {
		t.Fatal(err)
	}
	other := newSession(t, "b", "sleep", "60")
	hosttest.OldName(t, other, obj.ID)
	var shown struct{ ID string }
	if err := json.Unmarshal([]byte(okJSON(t, "show", obj.Address, "--json")), &shown); err != nil || shown.ID != obj.ID {
		t.Fatalf("show %s --json: %+v, %v", obj.Address, shown, err)
	}
	okSaid(t, "rm", obj.Address, "-y")
	list := liveList(t, th)
	if len(list) != 1 || list[0].ID != other {
		t.Errorf("rm %s deleted another session: %+v", obj.Address, list)
	}
}

// The commands that a running session's verbs print name it by host/ID,
// when its name, cleaned, is another session's name: debug start's line
// to stop, forward's Try: line, and show's attach line.
func TestTheVerbsPrintTheID(t *testing.T) {
	th := newTestHost(t)
	a := newSession(t, "auth\u202e", "sleep", "60")
	newSession(t, "auth", "sleep", "60")
	list := liveList(t, th)
	out := okSaid(t, "debug", "start", "box/"+a, "--for", "1m")
	_, stop, found := strings.Cut(out, "To stop: ")
	if !found {
		t.Fatalf("debug start printed %q", out)
	}
	findsSession(t, "debug start", strings.TrimSpace(stop), "box", list, a, false)
	_, err := run(t, "forward", "box/"+a, "x")
	findsSession(t, "forward", tryOf(t, err), "box", list, a, false)
	for _, l := range strings.Split(ok(t, "show", "box/"+a), "\n") {
		if rest, found := strings.CutPrefix(strings.TrimSpace(l), "Attach"); found {
			findsSession(t, "show", strings.TrimSpace(rest), "box", list, a, false)
			if !strings.Contains(rest, brand.Name+" attach box/"+a) {
				t.Errorf("show's attach line: %q", rest)
			}
		}
	}
}

// new and resume without --detach attach, so without a terminal they fail
// before any lookup and any remote work (cli-spec 9.3, rule 2): no
// connection, no session started, and none resumed.
func TestNewAndResumeCheckTheTerminalFirst(t *testing.T) {
	th := newTestHost(t)
	endedOnce(t, th, "crash")
	id := idOf(t, th, "crash")
	before := len(th.tr.Remotes())
	// The Try: line adds --detach to the typed line, which names a session
	// only by host/ID (cli-spec 9.2): a name gives the list of sessions.
	for _, c := range []struct {
		args []string
		try  string
	}{
		{[]string{"new", "box", "--name", "fresh", "--", "sleep", "60"}, brand.Name + " new box --name fresh --detach -- sleep 60"},
		{[]string{"resume", "box/" + id}, brand.Name + " resume box/" + id + " --detach"},
		{[]string{"resume", "crash"}, brand.Name + " ls"},
	} {
		text, code := failsAs(t, c.args...)
		if code != 2 || !strings.Contains(text, "attaches, so it needs a terminal") || !strings.Contains(text, "Try: "+c.try+"\n") {
			t.Errorf("%s without a terminal: exit %d\n%s", strings.Join(c.args, " "), code, text)
		}
	}
	if n := len(th.tr.Remotes()) - before; n != 0 {
		t.Errorf("new and resume without a terminal ran %d commands on the host: %q", n, th.tr.Remotes()[before:])
	}
	list := liveList(t, th)
	if len(list) != 1 || list[0].ID != id || !client.Ended(list[0]) {
		t.Errorf("after new and resume without a terminal: %+v", list)
	}
}

// A session that new names after its folder, when the folder's name is the
// ID of an older helper's session: the name gets "-s", so an address with
// the name reaches the new session, and the older ID the older one.
func TestAFolderNamedLikeAnOlderID(t *testing.T) {
	th := newTestHost(t)
	if err := (&host.Manifest{Schema: 1, ID: "oldoldid", Name: "old"}).Save(); err != nil {
		t.Fatal(err)
	}
	var obj struct{ ID, Name string }
	if err := json.Unmarshal([]byte(okJSON(t, "new", "box", "--detach", "--json", "--dir", "~/oldoldid", "--mkdir", "--", "sleep", "60")), &obj); err != nil ||
		obj.Name != "oldoldid-s" {
		t.Fatalf("a session in ~/oldoldid: %+v, %v", obj, err)
	}
	if out := okSaid(t, "rm", "box/"+obj.Name, "-y"); !strings.HasPrefix(out, "Deleted oldoldid-s ("+obj.ID+") on box.") {
		t.Errorf("rm box/%s -y: %q", obj.Name, out)
	}
	if list := liveList(t, th); len(list) != 1 || list[0].ID != "oldoldid" {
		t.Errorf("after the rm of the new session: %+v", list)
	}
}

// A new session's ID looks like an ID, never equals an existing session's
// name, and reaches the new session alone by host/ID, including attach.
func TestNewSessionIDIsExactForAttachAndShow(t *testing.T) {
	th := newTestHost(t)
	t.Setenv(noInputEnv, "")
	older := newSession(t, "old", "sleep", "60")
	hosttest.OldName(t, older, "abcd2345")
	named := newSession(t, "frontend", "sleep", "60")
	var obj struct{ ID, Address string }
	if err := json.Unmarshal([]byte(okJSON(t, "new", "box", "--detach", "--json", "--name", "fresh", "--", "/bin/sh", "-c", "echo EXACT-99; exec sleep 60")), &obj); err != nil {
		t.Fatal(err)
	}
	if !api.LooksLikeID(obj.ID) || obj.ID == "abcd2345" || obj.ID == "frontend" || obj.ID == older || obj.ID == named {
		t.Fatalf("new chose %+v", obj)
	}
	term, done := inTerminal(t, "attach", obj.Address)
	term.WaitFor("EXACT-99")
	term.Type("\x1c")
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if text, code := failsAs(t, "show", "box/abcd2345"); code != 3 || !strings.Contains(text, `no session has the ID "abcd2345" on box`) {
		t.Errorf("show box/abcd2345: exit %d\n%s", code, text)
	}
	okSaid(t, "rm", obj.Address, "-y")
	if list := liveList(t, th); len(list) != 2 {
		t.Errorf("after rm %s: %+v", obj.Address, list)
	}
}
