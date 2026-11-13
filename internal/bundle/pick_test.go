package bundle

import (
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// debug bundle <host>/<word> follows the addresses' rule (cli-spec 9.2): a
// word of the form of an ID finds an ID alone, never a name that an older
// helper allowed. Any other word finds an ID, then a name.
func TestPickFollowsTheRuleOfAddresses(t *testing.T) {
	list := []client.Session{{ID: "zzzz2222", Name: "abcd2345"}, {ID: "abcd2346", Name: "frontend"}, {ID: "oldoldid", Name: "x"}}
	_, err := pick(list, "abcd2345", "box")
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Code != "session_not_found" || ue.Msg != `no session has the ID "abcd2345" on box` {
		t.Errorf("a word of an ID's form that only a name has: %v", err)
	}
	for ref, want := range map[string]string{"zzzz2222": "zzzz2222", "frontend": "abcd2346", "oldoldid": "oldoldid", "x": "oldoldid"} {
		if s, err := pick(list, ref, "box"); err != nil || s.ID != want {
			t.Errorf("pick(%q) = %+v, %v, want %s", ref, s, err, want)
		}
	}
}

// The hint that runs a bundle again names its session by the ID that the
// bundle found, an older ID without a digit too. Before the bundle found
// it, the hint lists the host's sessions: the typed word is never
// repeated, and never stands as a placeholder in a command.
func TestTheHintThatRunsABundleAgain(t *testing.T) {
	box := &client.Host{Name: "box"}
	for _, c := range []struct {
		o     Options
		found string
		want  string
	}{
		{Options{Host: box, Session: "frontend"}, "", brand.Name + " ls box (to find the session's ID, then start the bundle again with it)"},
		{Options{Host: box, Session: "abcd2345"}, "", brand.Name + " ls box (to find the session's ID, then start the bundle again with it)"},
		{Options{Host: box, Session: "frontend"}, "abcd2346", brand.Name + " debug bundle box/abcd2346 (to start it again)"},
		{Options{Host: box, Session: "x"}, "oldoldid", brand.Name + " debug bundle box/oldoldid (to start it again)"},
		{Options{Host: box, Session: "x"}, "a b;c", brand.Name + " ls box (to find the session's ID, then start the bundle again with it)"},
		{Options{Host: box}, "", brand.Name + " debug bundle box (to start it again)"},
		{Options{}, "", brand.Name + " debug bundle (to start it again)"},
		// An ID so long that the error's line would cut it: the cut ID
		// could name another session.
		{Options{Host: box, Session: "x"}, strings.Repeat("a", 3980), brand.Name + " ls box (to find the session's ID, then start the bundle again with it)"},
		// A host's name that could not go in a command.
		{Options{Host: &client.Host{Name: "b;x"}, Session: "x"}, "abcd2345", brand.Name + " ls (to find the session's host and ID, then start the bundle again with them)"},
		{Options{Host: &client.Host{Name: "-box"}}, "", brand.Name + " ls (to find the host, then start the bundle again with it)"},
	} {
		got := again(c.o, c.found)
		if got != c.want {
			t.Errorf("again(%q, %.20q) = %.60q, want %q", c.o.Session, c.found, got, c.want)
		}
		// The error shows the hint as it is.
		if api.CleanText(got, 4000) != got {
			t.Errorf("again(%q, %.20q): the error's line would change the hint", c.o.Session, c.found)
		}
	}
}
