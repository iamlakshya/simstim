package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
)

// The address rules of cli-spec 9.2, as resolveAddress reads them for a
// command line, before the command dials, and as the host's live list then
// decides them.

// addressOf resolves the address of the command line args, as its command
// would: rm and kill strictly. It returns what the lookup said on stderr.
func addressOf(t *testing.T, args ...string) (address, string, error) {
	t.Helper()
	cmd, rest, err := Root().Find(args)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.ParseFlags(rest); err != nil {
		t.Fatal(err)
	}
	cmd.SetContext(withLine(context.Background(), args))
	strict := cmd.Name() == "rm" || cmd.Name() == "kill"
	var a address
	_, errOut := captureOutput(t, func() { a, err = resolveAddress(cmd, cmd.Flags().Arg(0), strict) })
	return a, errOut, err
}

// reportOf is what printError prints for err, and the exit code.
func reportOf(err error) (string, int) {
	var b bytes.Buffer
	code := printError(&b, err, nil, false, false)
	return b.String(), code
}

// failsAs runs the command line, and returns its error as printError
// prints it, and the exit code.
func failsAs(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd, err := line(t, args...)
	if err == nil {
		t.Fatalf("%s: no error", strings.Join(args, " "))
	}
	var b bytes.Buffer
	code := printError(&b, err, cmd, false, false)
	return b.String(), code
}

// twoHosts is the test host as two saved hosts, box and nuc, which list
// the same sessions. nuc's transport can be made to fail.
func twoHosts(t *testing.T) *hosttest.Transport {
	t.Helper()
	th := newTestHost(t)
	nuc, tr := th.h.ClientHost("nuc")
	if err := client.SaveHostsContext(context.Background(), []client.Host{th.ch, nuc}); err != nil {
		t.Fatal(err)
	}
	return tr
}

// bridges counts the connections that the client opened to a host.
func bridges(tr *hosttest.Transport) int {
	n := 0
	for _, r := range tr.Remotes() {
		if strings.HasSuffix(r, " bridge") || strings.Contains(r, " bridge ") {
			n++
		}
	}
	return n
}

// otherClient runs fn with a connection of its own to the test host, as
// another client, such as the launcher on another computer, would. It
// changes the host, and not this client's session cache.
func otherClient(t *testing.T, th *testHost, fn func(context.Context, *client.Conn) error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := client.Dial(ctx, th.ch, false)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := fn(ctx, conn); err != nil {
		t.Fatal(err)
	}
}

// renamed waits until the host lists a session named name.
func renamed(t *testing.T, th *testHost, name string) {
	t.Helper()
	hosttest.WaitFor(t, 5*time.Second, "the name "+name, func() bool { return idOf(t, th, name) != "" })
}

// idOf is the ID of the session named name on the test host.
func idOf(t *testing.T, th *testHost, name string) string {
	t.Helper()
	var id string
	otherClient(t, th, func(ctx context.Context, conn *client.Conn) error {
		list, err := conn.Sessions(ctx)
		for _, s := range list {
			if s.Name == name {
				id = s.ID
			}
		}
		return err
	})
	return id
}

// "-" is the session last attached to, and without one it is not found.
// Only a host that is no longer saved changes the error: a hosts.toml that
// does not decode stays a usage error.
func TestAddressDash(t *testing.T) {
	newTestHost(t)
	_, _, err := addressOf(t, "attach", "-")
	if got, code := reportOf(err); code != 3 || !strings.Contains(got, "Error: no session has been attached yet.") || !strings.Contains(got, "Try: "+brand.Name+" ls\n") {
		t.Errorf("- with no last session: exit %d\n%s", code, got)
	}
	client.RememberLast("box", "a1b2", "web")
	if a, _, err := addressOf(t, "send", "-", "hi"); err != nil || a.host.Name != "box" || a.ref != "a1b2" {
		t.Errorf("- = %+v, %v", a, err)
	}
	// The last session is gone: the error names it by its name.
	got, code := failsAs(t, "show", "-")
	if code != 3 || !strings.Contains(got, "Error: the last session, web on box, is gone.") || strings.Contains(got, "a1b2") {
		t.Errorf("show - after the session went: exit %d\n%s", code, got)
	}
	client.RememberLast("gone", "a1b2", "web")
	if _, _, err := addressOf(t, "show", "-"); err == nil || !strings.Contains(err.Error(), `"gone", which is no longer a saved host`) {
		t.Errorf("- on a host that was removed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(client.ConfigDir(), "hosts.toml"), []byte("[[host]]\nname = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = addressOf(t, "show", "-")
	if got, code := reportOf(err); code != 2 || !strings.Contains(got, "hosts.toml") || strings.Contains(got, "no longer a saved host") {
		t.Errorf("- with a hosts.toml that does not decode: exit %d\n%s", code, got)
	}
}

// host/session names its host, and the session part is found on it later.
func TestAddressWithHost(t *testing.T) {
	newTestHost(t)
	if a, _, err := addressOf(t, "show", "box/auth-fix"); err != nil || a.host.Name != "box" || a.ref != "auth-fix" {
		t.Errorf("box/auth-fix = %+v, %v", a, err)
	}
	_, _, err := addressOf(t, "show", "bx/auth-fix")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "host_not_found" || exitCode(ue.Kind) != 3 {
		t.Errorf("bx/auth-fix = %v", err)
	}
}

// A bare word on one host gives that host, and keeps the word, by a name or
// an ID: the host's live list finds the session. A session named like a
// host is a session.
func TestAddressWithoutHost(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "auth-fix", "sleep", "60")
	newSession(t, "box", "sleep", "60")
	for _, word := range []string{"auth-fix", id, "box"} {
		if a, errOut, err := addressOf(t, "attach", word); err != nil || a.host.Name != "box" || a.ref != word || errOut != "" {
			t.Errorf("%s = %+v, %v (stderr %q), want box and the word", word, a, err, errOut)
		}
	}
	if out := ok(t, "show", "box"); !strings.HasPrefix(out, "box on box\n") {
		t.Errorf("show box:\n%s", out)
	}
	// No such session: not found, with the sessions it may mean, by ID.
	_, _, err := addressOf(t, "attach", "auth-fx")
	got, code := reportOf(err)
	if code != 3 || !strings.Contains(got, `no session named "auth-fx" on any host`) || !strings.Contains(got, `Did you mean "box/`+id+`" (auth-fix)?`) ||
		!strings.Contains(got, "Try: "+brand.Name+" ls\n") {
		t.Errorf("auth-fx: exit %d\n%s", code, got)
	}
}

// The reviewer's probe (blocker R1): the cache saw a, then a was renamed b
// and a new a started. rm a -y deletes the new a, never b. With another
// client making the changes, so that this client's cache does not know
// them, the host's live list still decides. A not-found error names only
// the word that the user typed.
func TestAddressFollowsTheLiveList(t *testing.T) {
	th := newTestHost(t)
	newSession(t, "a", "sleep", "60")
	ok(t, "ls")
	okSaid(t, "rename", "box/a", "b")
	renamed(t, th, "b")
	b := idOf(t, th, "b")
	newSession(t, "a", "sleep", "60")
	newA := idOf(t, th, "a")
	if out := okSaid(t, "rm", "a", "-y"); out != "Deleted a ("+newA+") on box.\n" {
		t.Errorf("rm a -y = %q, want the new a (%s), not b (%s)", out, newA, b)
	}
	if !strings.Contains(ok(t, "ls"), b) {
		t.Fatal("rm a -y deleted b")
	}

	// Another client renames b to c, and starts a new b: the cache still has
	// the old b, and is fresh.
	ok(t, "ls")
	otherClient(t, th, func(ctx context.Context, conn *client.Conn) error { return conn.RenameSession(ctx, b, "c") })
	renamed(t, th, "c")
	got, code := failsAs(t, "show", "b")
	if first, _, _ := strings.Cut(got, "\n"); code != 3 || !strings.Contains(first, `no session "b" on box`) || strings.Contains(first, b) {
		t.Errorf("show b after another client renamed it: exit %d, and the error must name only the word typed:\n%s", code, got)
	}
	// The live list went to the cache: the next lookup knows no b.
	if got, code := failsAs(t, "show", "b"); code != 3 || !strings.Contains(got, `no session named "b" on any host`) || strings.Contains(strings.SplitN(got, "\n", 2)[0], b) {
		t.Errorf("show b again: exit %d\n%s", code, got)
	}
	otherClient(t, th, func(ctx context.Context, conn *client.Conn) error {
		_, err := conn.NewSession(ctx, client.NewArgs{Name: "b", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24})
		return err
	})
	newB := idOf(t, th, "b")
	if out := okSaid(t, "rm", "b", "-y"); out != "Deleted b ("+newB+") on box.\n" {
		t.Errorf("rm b -y = %q, want the new b (%s), not c (%s)", out, newB, b)
	}
}

// A list in the cache that is younger than 60 seconds may lack a session
// that was made since: before it fails, the lookup asks the hosts again
// (major R2). new, rm and rename change the cache too.
func TestAddressAsksAgainOnAMiss(t *testing.T) {
	th := newTestHost(t)
	ok(t, "ls")
	newSession(t, "fresh", "sleep", "60")
	if c := client.LoadSessionCache(); len(c.Sessions["box"]) != 1 || c.Sessions["box"][0].Name != "fresh" {
		t.Errorf("new did not reach the cache: %+v", c.Sessions)
	}
	if out := ok(t, "show", "fresh"); !strings.HasPrefix(out, "fresh on box\n") {
		t.Errorf("show fresh after new:\n%s", out)
	}
	okSaid(t, "rename", "fresh", "renamed")
	renamed(t, th, "renamed")
	if c := client.LoadSessionCache(); len(c.Sessions["box"]) != 1 || c.Sessions["box"][0].Name != "renamed" {
		t.Errorf("rename did not reach the cache: %+v", c.Sessions)
	}
	okSaid(t, "rm", "renamed", "-y")
	if c := client.LoadSessionCache(); len(c.Sessions["box"]) != 0 {
		t.Errorf("rm did not reach the cache: %+v", c.Sessions)
	}

	// Another client starts a session: the fresh cache lacks it.
	ok(t, "ls")
	otherClient(t, th, func(ctx context.Context, conn *client.Conn) error {
		_, err := conn.NewSession(ctx, client.NewArgs{Name: "elsewhere", Argv: []string{"sleep", "60"}, Cols: 80, Rows: 24})
		return err
	})
	if out := ok(t, "show", "elsewhere"); !strings.HasPrefix(out, "elsewhere on box\n") {
		t.Errorf("show elsewhere, which the fresh cache lacked:\n%s", out)
	}
}

// A name on two hosts fails with both full addresses, by host/ID, and the
// Try: line is the command line with the first (the third acceptance line
// of C5).
func TestAddressOnTwoHosts(t *testing.T) {
	twoHosts(t)
	id := newSession(t, "a", "sleep", "60")
	_, _, err := addressOf(t, "attach", "a")
	got, code := reportOf(err)
	if code != 2 || !strings.Contains(got, `Error: 2 sessions are named "a": box/`+id+`, nuc/`+id+`.`) || !strings.Contains(got, "Try: "+brand.Name+" attach box/"+id+"\n") {
		t.Errorf("a on two hosts: exit %d\n%s", code, got)
	}
	_, _, err = addressOf(t, "debug", "start", "a", "--for", "1m")
	if got, _ := reportOf(err); !strings.Contains(got, "Try: "+brand.Name+" debug start box/"+id+" --for 1m\n") {
		t.Errorf("the Try: line of debug start:\n%s", got)
	}
	// An old name's Try: line is in its new form.
	_, _, err = addressOf(t, "kill", "a")
	if got, _ := reportOf(err); !strings.Contains(got, "Try: "+brand.Name+" rm box/"+id+" -y\n") {
		t.Errorf("the Try: line of kill:\n%s", got)
	}
	_, _, err = addressOf(t, "flip", "a")
	if got, _ := reportOf(err); !strings.Contains(got, "Try: "+brand.Name+" attach box/"+id+"\n") {
		t.Errorf("the Try: line of flip:\n%s", got)
	}
}

// An ID wins over a name on its own host only: an ID on one host and the
// same word as a name on another are two matches (major X1). That holds
// for an ID of another form. A word of the form of the helper's IDs is an
// ID alone, so a name of that form on another host does not count
// (cli-spec 9.2).
func TestAddressIDOnOneHostNameOnAnother(t *testing.T) {
	hosts := []client.Host{{Name: "box"}, {Name: "nuc"}}
	lists := map[string][]client.Session{"box": {{ID: "x1", Name: "a"}}, "nuc": {{ID: "n1", Name: "x1"}}}
	got, code := reportOf(ambiguousMatches(nil, "x1", matchesOf(hosts, lists, "x1")))
	if code != 2 || !strings.Contains(got, `2 sessions match "x1": box/x1, nuc/n1.`) {
		t.Errorf("an ID of another form on box, and a name on nuc: exit %d\n%s", code, got)
	}
	nuc := twoHosts(t)
	id := newSession(t, "a", "sleep", "60")
	nuc.SetDown(errors.New("nuc is down"))
	writeCache(t, fmt.Sprintf(`{"sessions":{"nuc":[{"id":"n2n2n2n2","name":%q,"state":"idle"}]},"seen":{"nuc":"2026-01-01T00:00:00Z"}}`, id))
	if a, _, err := addressOf(t, "attach", id); err != nil || a.host.Name != "box" || a.ref != id {
		t.Errorf("an ID on box, and a name of that ID on nuc: %+v, %v", a, err)
	}
}

// Two sessions of one name on one host: the host's live list finds both,
// and the command fails with the address of each by ID (cli-spec 9.2).
func TestAddressTwoOnOneHost(t *testing.T) {
	newTestHost(t)
	first := newSession(t, "dup", "sleep", "60")
	second := newSession(t, "dup", "sleep", "60")
	for _, args := range [][]string{{"show", "dup"}, {"show", "box/dup"}} {
		got, code := failsAs(t, args...)
		if code != 2 || !strings.Contains(got, `2 sessions are named "dup" on box: box/`) || !strings.Contains(got, "box/"+first) ||
			!strings.Contains(got, "box/"+second) || !strings.Contains(got, "Try: "+brand.Name+" show box/") {
			t.Errorf("%s: exit %d\n%s", args, code, got)
		}
	}
	_, notFound := run(t, "show", "box/dupp")
	if got, code := reportOf(notFound); code != 3 || !strings.Contains(got, `"box/`+first+`" (dup)`) || !strings.Contains(got, `"box/`+second+`" (dup)`) {
		t.Errorf("show box/dupp: exit %d\n%s", code, got)
	}
}

// writeSeen writes the session cache with box's list, seen at the time at.
func writeSeen(t *testing.T, at time.Time, sessions string) {
	t.Helper()
	writeCache(t, fmt.Sprintf(`{"sessions":{"box":%s},"seen":{"box":%q}}`, sessions, at.UTC().Format(time.RFC3339Nano)))
}

// A list in the cache that is younger than 60 seconds names the host to
// ask without asking. An older one, or one from the future, is asked for
// again, and the answer goes to the cache.
func TestAddressCacheAge(t *testing.T) {
	th := newTestHost(t)
	const cached = `[{"id":"zz9","name":"cached-only","agent":"claude","state":"idle","created_at":"2026-11-10T10:00:00Z","alive":true}]`
	writeSeen(t, time.Now(), cached)
	before := bridges(th.tr)
	if a, _, err := addressOf(t, "attach", "cached-only"); err != nil || a.ref != "cached-only" || bridges(th.tr) != before {
		t.Errorf("a fresh cache: %+v, %v, and %d connections", a, err, bridges(th.tr)-before)
	}
	for _, at := range []time.Time{time.Now().Add(-2 * time.Minute), time.Now().Add(time.Hour)} {
		writeSeen(t, at, cached)
		before = bridges(th.tr)
		_, _, err := addressOf(t, "attach", "cached-only")
		if _, code := reportOf(err); code != 3 || bridges(th.tr) == before {
			t.Errorf("a cache seen at %v: exit %d, %v, and %d connections", at, code, err, bridges(th.tr)-before)
		}
		if c := client.LoadSessionCache(); len(c.Sessions["box"]) != 0 || time.Since(c.Seen["box"]) > time.Minute {
			t.Errorf("the answer did not reach the cache: %+v", c)
		}
	}
}

// A host that does not answer counts with its cached list, and is named on
// stderr. attach goes on with the one match. rm does not, because that
// host may have a session of the same name: it fails with exit code 2, and
// the Try: line holds the host and the session's ID.
func TestAddressWhenAHostDoesNotAnswer(t *testing.T) {
	nuc := twoHosts(t)
	id := newSession(t, "a", "sleep", "60")
	nuc.SetDown(errors.New("nuc is down"))
	a, errOut, err := addressOf(t, "attach", "a")
	if err != nil || a.host.Name != "box" || a.ref != "a" || !strings.Contains(errOut, "nuc did not answer, so its sessions are as last seen.") {
		t.Errorf("attach a: %+v, %v, stderr %q", a, err, errOut)
	}
	_, _, err = addressOf(t, "rm", "a", "-y")
	got, code := reportOf(err)
	if code != 2 || !strings.Contains(got, `"a" is on box, but nuc did not answer, so it may have a session of that name too.`) ||
		!strings.Contains(got, "Try: "+brand.Name+" rm box/"+id+" -y\n") {
		t.Errorf("rm a: exit %d\n%s", code, got)
	}

	// Its cached list still counts: a session that only it had is found.
	// rm says that the list of that host may be out of date.
	writeCache(t, `{"sessions":{"nuc":[{"id":"n1","name":"only-nuc","state":"idle"}]},"seen":{"nuc":"2026-01-01T00:00:00Z"}}`)
	if a, _, err := addressOf(t, "show", "only-nuc"); err != nil || a.host.Name != "nuc" || a.ref != "only-nuc" {
		t.Errorf("a session in the cache of a host that does not answer: %+v, %v", a, err)
	}
	_, _, err = addressOf(t, "rm", "only-nuc", "-y")
	if got, code := reportOf(err); code != 2 || !strings.Contains(got, `"only-nuc" is on nuc as last seen, but nuc did not answer, so its list may be out of date.`) {
		t.Errorf("rm only-nuc: exit %d\n%s", code, got)
	}
}

// The launcher saved a host's list with the time it arrived, over a minute
// ago, and the host is down now (major X2). A bare rm asks the host again,
// and refuses, because the host did not answer.
func TestAddressAfterTheLauncherSavedAnOldList(t *testing.T) {
	th := newTestHost(t)
	newSession(t, "a", "sleep", "60")
	ok(t, "ls")
	list := client.LoadSessionCache().Sessions["box"]
	writeCache(t, `{}`)
	client.RememberLists(map[string]client.HostList{"box": {Sessions: list, At: time.Now().Add(-2 * time.Minute)}})
	th.tr.SetDown(errors.New("box is down"))
	got, code := failsAs(t, "rm", "a", "-y")
	if code != 2 || !strings.Contains(got, `"a" is on box as last seen, but box did not answer, so its list may be out of date.`) {
		t.Errorf("rm a with box down: exit %d\n%s", code, got)
	}
}

// attach needs a terminal, and checks for one before any lookup: without
// one it opens no connection to a host (X5), and fails with exit code 2
// and the code of every other command that needs a terminal. Its Try:
// line repeats no name that it did not look up.
