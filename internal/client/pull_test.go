package client

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The answer of the helper's repo.info becomes what pull needs, and each
// error becomes one with a hint that names the host.
func TestParseRepoInfo(t *testing.T) {
	h := Host{Name: "box"}
	r, err := parseRepoInfo(h, "~/src/app", []byte(`{"repo":{"root":"/home/dev/src/app","main":"/home/dev/src/app","branch":"auth-fix","dirty":3}}`+"\n"))
	if err != nil || r != (RepoInfo{Root: "/home/dev/src/app", Main: "/home/dev/src/app", Branch: "auth-fix", Dirty: 3}) {
		t.Fatalf("%+v %v", r, err)
	}
	for _, c := range []struct{ out, msg, hint string }{
		{`{"error":{"code":"not_a_repo","message":"~ is not inside a git repository"}}`, "~/src/app on box is not in a git repository", "Start the session in a repository"},
		{`{"error":{"code":"no_git","message":"git is not installed on this host"}}`, "git is not installed on box", "Install git on box"},
		{`{"error":{"code":"git_scope","message":"could not start git in a scope on this host: Failed to connect to bus"}}`, "on box: Failed to connect to bus", brand.Name + " host doctor box"},
		{`{"error":{"code":"git","message":"git status: fatal: bad index"}}`, "bad index", "git status on box"},
		{"fatal: not a git repository\n", "could not read what box said", "host add box"},
		{`{}`, "could not read what box said", "host add box"},
	} {
		_, err := parseRepoInfo(h, "~/src/app", []byte(c.out))
		var ue *UserError
		if !errors.As(err, &ue) || !strings.Contains(ue.Msg, c.msg) || !strings.Contains(ue.Hint, c.hint) {
			t.Errorf("%s: %v, want %q with a hint %q", c.out, err, c.msg, c.hint)
		}
	}
}

// The helper's git errors say "on this host". The client names the host,
// and a git_scope error's hint runs the doctor there. Other errors pass.
func TestHostError(t *testing.T) {
	for _, c := range []struct {
		in        proto.Error
		msg, hint string
	}{
		{proto.Error{Code: api.CodeGitScope, Message: "could not start git in a scope on this host: Failed to connect to bus", Hint: "Run the doctor."},
			"could not start git in a scope on box: Failed to connect to bus", brand.Name + " host doctor box"},
		{proto.Error{Code: api.CodeGitTimeout, Message: "git ran longer than 1m0s in repo.info on this host, so the helper stopped it", Hint: "A filter may hang."},
			"git ran longer than 1m0s in repo.info on box, so the helper stopped it", "A filter may hang."},
		{proto.Error{Code: api.CodeGitHooks, Message: "git's config in /r on this host defines a hook named \"a=b\", which the helper cannot turn off", Hint: "Remove it."},
			"git's config in /r on box defines a hook named \"a=b\", which the helper cannot turn off", "Remove it."},
		{proto.Error{Code: "not_found", Message: "no session on this host", Hint: "List them."}, "no session on this host", "List them."},
	} {
		ue := hostErrorOn("box", &c.in)
		if ue.Msg != c.msg || ue.Hint != c.hint || ue.Code != c.in.Code {
			t.Errorf("hostError(%+v) = %+v, want %q, %q", c.in, ue, c.msg, c.hint)
		}
	}
}

func TestFetchArgs(t *testing.T) {
	h := Host{Name: "box", SSH: "dev@box"}
	got := FetchArgs(h, "/home/dev/src/app", "auth-fix", "review/auth-fix", false)
	want := []string{"fetch", "--no-tags", "dev@box:/home/dev/src/app", "refs/heads/auth-fix:refs/heads/review/auth-fix"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := FetchArgs(h, "/r", "b", "b", true); got[3] != "+refs/heads/b:refs/heads/b" {
		t.Errorf("force: %q", got[3])
	}
}

func TestShellWord(t *testing.T) {
	for in, want := range map[string]string{"plain": "plain", "a b": "'a b'", "it's": `'it'\''s'`, "ControlPath=/x/%C": "'ControlPath=/x/%C'"} {
		if got := shellWord(in); got != want {
			t.Errorf("shellWord(%q) = %q, want %q", in, got, want)
		}
	}
}
