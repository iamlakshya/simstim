//go:build unix

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/tui"
)

// Sessions that change under a command: a known ID that a name could
// shadow, a rename while rm's question waits, lists that arrive late, and
// an agent that ends again while another client starts it.

// A known ID, from "-" or a picked row, finds its session by its ID alone.
// A session named like that ID, as an older helper allowed, is never taken
// for it (V1).
func TestKnownIDsNeverMatchNames(t *testing.T) {
	newTestHost(t)
	const id = "abcdefgh"
	named := newSession(t, "x", "sleep", "60")
	hosttest.OldName(t, named, id)
	client.RememberLast("box", id, "old")
	got, code := failsAs(t, "show", "-")
	if code != 3 || !strings.Contains(got, "Error: the last session, old on box, is gone.") {
		t.Errorf("show - with a session named like the last ID: exit %d\n%s", code, got)
	}
	if _, code := failsAs(t, "rm", "-", "-y"); code != 3 {
		t.Errorf("rm - -y: exit %d", code)
	}
	a, err := pickedAddress(tui.PickRow{Host: "box", Session: client.Session{ID: id, Name: "old"}})
	if err != nil {
		t.Fatal(err)
	}
	cmd, _, _ := Root().Find([]string{"rm"})
	err = a.open(context.Background(), cmd, func(context.Context, found) error { return errors.New("the picked row found a session") })
	if got, code := reportOf(err); code != 3 || !strings.Contains(got, "old on box is gone") {
		t.Errorf("a picked row whose session went: exit %d\n%s", code, got)
	}
	if !strings.Contains(ok(t, "ls"), named) {
		t.Error("the session named like the ID was deleted")
	}
}

// A rename while rm's question waits: after the answer, rm looks again,
// and deletes nothing, because the session is not the one confirmed (V2).
// Another client also starts a new session with the old name: it stays.
func TestRmRenameWhileTheQuestionWaits(t *testing.T) {
	th := newTestHost(t)
	t.Setenv(noInputEnv, "")
	id := newSession(t, "a", "sleep", "60")
	old := afterQuestion
	t.Cleanup(func() { afterQuestion = old })
	afterQuestion = func(found) {
		otherClient(t, th, func(ctx context.Context, conn *client.Conn) error {
			if err := conn.RenameSession(ctx, id, "b"); err != nil {
				return err
			}
			_, err := conn.NewSession(ctx, client.NewArgs{Name: "a", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24, Wall: "none", Downgrade: true})
			return err
		})
		renamed(t, th, "b")
	}
	term, done := inTerminal(t, "rm", "box/a")
	term.WaitFor("Delete it? [y/N] ")
	term.Type("y\r")
	got, code := reportOf(wait(t, done))
	if code != 1 || !strings.Contains(got, "Error: a on box was renamed b while the question waited, so it was not deleted.") {
		t.Errorf("rm after a rename: exit %d\n%s", code, got)
	}
	out := ok(t, "ls")
	if !strings.Contains(out, id) || strings.Count(out, " a ") != 1 {
		t.Errorf("rm deleted a session:\n%s", out)
	}
}

// The host refuses a delete whose expected name is not the session's, so
// a rename between the last look and the delete deletes nothing (V2).
func TestHostRefusesARenamedDelete(t *testing.T) {
	th := newTestHost(t)
	id := newSession(t, "a", "sleep", "60")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := client.Dial(ctx, th.ch, false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, err = conn.DeleteNamed(ctx, id, "b")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "session_renamed" || !strings.Contains(ue.Msg, `is now named "a", not "b"`) {
		t.Fatalf("a delete that expects another name: %v", err)
	}
	if !strings.Contains(ok(t, "ls"), id) {
		t.Fatal("the host deleted it")
	}
	if res, err := conn.DeleteNamed(ctx, id, "a"); err != nil || !res.Deleted {
		t.Errorf("a delete that expects its name: %+v, %v", res, err)
	}
}

// Each host's list keeps the time when it arrived, taken in its own
// worker. A host that answers late does not make an earlier list look
// newer than one that another writer saved in between (V3).
func TestALateHostKeepsTheTimeOfEachList(t *testing.T) {
	twoHosts(t)
	newSession(t, "a", "sleep", "60")
	boxDone := make(chan struct{})
	old := listSessions
	t.Cleanup(func() { listSessions = old })
	listSessions = func(ctx context.Context, conn *client.Conn) ([]client.Session, error) {
		if conn.Host.Name == "box" {
			defer close(boxDone)
			return old(ctx, conn)
		}
		<-boxDone
		time.Sleep(20 * time.Millisecond)
		// Another writer, such as the launcher, saves a newer list of box.
		client.RememberLists(map[string]client.HostList{"box": {Sessions: []client.Session{{ID: "newer"}}, At: time.Now()}})
		time.Sleep(20 * time.Millisecond)
		return old(ctx, conn)
	}
	ok(t, "ls")
	if c := client.LoadSessionCache(); len(c.Sessions["box"]) != 1 || c.Sessions["box"][0].ID != "newer" {
		t.Errorf("box's older list replaced the newer one: %+v", c.Sessions["box"])
	}
}

// Another client starts the agent again first, and the agent ends again
// before this client reads its row (V4): the row is read, not trusted to
// run, and the resume tries once more.
func TestAnotherClientResumesAndTheAgentEndsAgain(t *testing.T) {
	th := newTestHost(t)
	id := newSession(t, "short", "/bin/sh", "-c", "sleep 1; exit 3")
	agentEnded(t, id)
	oldResume, oldList := beforeResume, listSessions
	t.Cleanup(func() { beforeResume, listSessions = oldResume, oldList })
	first := true
	beforeResume = func(f found) {
		if !first {
			return
		}
		first = false
		otherClient(t, th, func(ctx context.Context, conn *client.Conn) error {
			_, err := conn.Resume(ctx, client.ResumeArgs{ID: f.s.ID})
			return err
		})
		// The next list comes after the other client's run has ended.
		listSessions = func(ctx context.Context, conn *client.Conn) ([]client.Session, error) {
			time.Sleep(2500 * time.Millisecond)
			listSessions = oldList
			return oldList(ctx, conn)
		}
	}
	out := okSaid(t, "resume", "box/short", "--detach")
	if !strings.HasPrefix(out, "Resumed box/"+id+" (short, shell)") || strings.Contains(out, "already running") {
		t.Errorf("resume --detach when the agent ended again: %q", out)
	}
}

// Every Try: line that an old command makes names the new form, and runs
// as written: it parses with the new command (V6).
func TestOldFormHintsParse(t *testing.T) {
	twoHosts(t)
	id := newSession(t, "a", "sleep", "60")
	var hints []string
	for _, args := range [][]string{{"kill", "a"}, {"kill", "--", "a"}, {"flip", "a"}} {
		_, _, err := addressOf(t, args...)
		var ue *client.UserError
		if !errors.As(err, &ue) {
			t.Fatalf("%s: %v", args, err)
		}
		hints = append(hints, ue.Hint)
	}
	for _, args := range [][]string{{"kill", "boxe/a"}, {"flip", "boxe/a"}, {"kill", "--bogus"}, {"kill"}, {"flip", "box/a", "--restrt"}} {
		cmd, err := line(t, args...)
		hints = append(hints, describe(err, cmd).try)
	}
	flip, _, _ := Root().Find([]string{"flip"})
	flip.SetContext(withLine(context.Background(), []string{"flip", "box/a", "--no-input"}))
	_ = flip.ParseFlags([]string{"box/a", "--no-input"})
	var ue *client.UserError
	if errors.As(prompts(flip).needs(qRestart, "a yes"), &ue) {
		hints = append(hints, ue.Hint)
	}
	if len(hints) != 9 {
		t.Fatalf("got %d hints", len(hints))
	}
	// -y goes before the "--" that ends the flags, and the line keeps it.
	if want := brand.Name + " rm -y -- box/" + id; hints[1] != want {
		t.Errorf("kill -- a: the hint is %q, want %q", hints[1], want)
	}
	for _, h := range hints {
		words := strings.Fields(strings.TrimPrefix(h, brand.Name+" "))
		if !strings.HasPrefix(h, brand.Name+" ") || len(words) == 0 || words[0] == "kill" || words[0] == "flip" || !parses(words) {
			t.Errorf("the hint %q does not name a new form that parses", h)
		}
	}
}

// A rename after rm's last look, before its delete request: the host
// catches it, because the request carries the name that rm saw (V2). A
// plain delete by ID would remove the renamed session.
func TestTheHostCatchesARenameAfterRmsLastLook(t *testing.T) {
	th := newTestHost(t)
	id := newSession(t, "a", "sleep", "60")
	old := beforeDelete
	t.Cleanup(func() { beforeDelete = old })
	beforeDelete = func(found) {
		otherClient(t, th, func(ctx context.Context, conn *client.Conn) error { return conn.RenameSession(ctx, id, "b") })
	}
	got, code := failsAs(t, "rm", "box/a", "-y")
	if code != 1 || !strings.Contains(got, `is now named "b", not "a", so it was not deleted`) {
		t.Errorf("rm -y after a rename: exit %d\n%s", code, got)
	}
	if !strings.Contains(ok(t, "ls"), id) {
		t.Error("rm deleted the renamed session")
	}
	// A session with no name, as an older helper allowed: the empty name
	// is the name that rm saw, and a name that it got since stops the
	// delete too.
	unnamed := newSession(t, "c", "sleep", "60")
	hosttest.OldName(t, unnamed, "")
	beforeDelete = func(found) {
		otherClient(t, th, func(ctx context.Context, conn *client.Conn) error { return conn.RenameSession(ctx, unnamed, "d") })
	}
	got, code = failsAs(t, "rm", "box/"+unnamed, "-y")
	if code != 1 || !strings.Contains(got, `is now named "d", not "", so it was not deleted`) {
		t.Errorf("rm -y of a session with no name, after a rename: exit %d\n%s", code, got)
	}
	if !strings.Contains(ok(t, "ls"), unnamed) {
		t.Error("rm deleted the session that got a name")
	}
}

// The JSON address of a resume is host/ID, so it finds the same session
// whatever another client does meanwhile (V3). Here the other client
// starts B, and renames A to B's name: by name, A's address would find
// two sessions.
// In the first case the other client also starts A again first, so the
// resume meets the host's refusal and reads A's row again. In the second,
// it does not, and the resume itself starts A.
func TestTheRaceKeepsTheAddressOfTheSession(t *testing.T) {
	for _, otherResumes := range []bool{true, false} {
		th := newTestHost(t)
		endedOnce(t, th, "a")
		a := idOf(t, th, "a")
		old := beforeResume
		beforeResume = func(f found) {
			beforeResume = old
			otherClient(t, th, func(ctx context.Context, conn *client.Conn) error {
				b, err := conn.NewSession(ctx, client.NewArgs{Name: "b", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24, Wall: "none", Downgrade: true})
				if err != nil {
					return err
				}
				if err := conn.RenameSession(ctx, a, b.Name); err != nil {
					return err
				}
				if otherResumes {
					_, err = conn.Resume(ctx, client.ResumeArgs{ID: a})
				}
				return err
			})
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(okJSON(t, "resume", "box/"+a, "--detach", "--json")), &obj); err != nil {
			t.Fatal(err)
		}
		beforeResume = old
		addr, _ := obj["address"].(string)
		var live []client.Session
		otherClient(t, th, func(ctx context.Context, conn *client.Conn) (err error) {
			live, err = conn.Sessions(ctx)
			return err
		})
		s, err := findIn(nil, live, strings.TrimPrefix(addr, "box/"), "box")
		if obj["id"] != a || addr != "box/"+a || err != nil || s.ID != a {
			t.Errorf("the other client resumes too: %v. The address %q of %v finds %+v, %v, want %s", otherResumes, addr, obj["id"], s, err, a)
		}
	}
}

// The attach command that show prints names the session itself, also when
// its name, cleaned for the terminal, is another session's name (V4).
func TestShowPrintsACommandForTheSameSession(t *testing.T) {
	th := newTestHost(t)
	a := newSession(t, "auth\u202e", "sleep", "60")
	newSession(t, "auth", "sleep", "60")
	out := ok(t, "show", "box/"+a)
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "Attach") {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "Attach"))
		}
	}
	words := strings.Fields(line)
	if len(words) != 3 || words[0] != brand.Name || !parses(words[1:]) {
		t.Fatalf("show printed the attach command %q:\n%s", line, out)
	}
	var live []client.Session
	otherClient(t, th, func(ctx context.Context, conn *client.Conn) (err error) {
		live, err = conn.Sessions(ctx)
		return err
	})
	if s, err := findIn(nil, live, strings.TrimPrefix(words[2], "box/"), "box"); err != nil || s.ID != a {
		t.Errorf("the printed command %q finds %+v, %v, want %s", line, s, err, a)
	}
}
