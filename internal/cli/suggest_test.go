package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/host/hosttest"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The whole list of suggestions, and the Try: line, for words that are
// not commands (docs/contributing/cli-spec.md 4.3).
func TestSuggestions(t *testing.T) {
	hosttest.New(t)
	help := func(c string) string { return strings.TrimSpace(brand.Name+" "+c) + " --help" }
	for _, c := range []struct {
		args []string
		want []string
		try  string
	}{
		{[]string{"list"}, []string{"ls"}, brand.Name + " ls"},
		{[]string{"run"}, []string{"new"}, help("new")},
		{[]string{"run", "box"}, []string{"new"}, brand.Name + " new box"},
		{[]string{"at"}, []string{"attach"}, help("attach")},
		{[]string{"ssh", "box"}, nil, help("")},
		{[]string{"kil"}, []string{"kill"}, help("kill")},
		{[]string{"LS"}, []string{"ls"}, brand.Name + " ls"},
		{[]string{"Attach"}, []string{"attach"}, help("attach")},
		{[]string{"Attach", "box/s"}, []string{"attach"}, brand.Name + " attach box/s"},
		{[]string{"x"}, nil, help("")},
		{[]string{"de"}, []string{"demo", "kill", "debug"}, brand.Name + " demo"},
		{[]string{"xyz"}, nil, help("")},
		{[]string{""}, nil, help("")},
		{[]string{"atach", "box/s"}, []string{"attach"}, brand.Name + " attach box/s"},
		{[]string{"delte", "box/s"}, []string{"kill"}, brand.Name + " kill box/s"},
		{[]string{"d"}, nil, help("")},
		{[]string{"help", "atach"}, []string{"attach"}, brand.Name + " help attach"},
		{[]string{"help", "host", "ad"}, []string{"add"}, brand.Name + " help host add"},
		{[]string{"help", "xyz"}, nil, help("")},
		{[]string{"hosts"}, []string{"host ls"}, brand.Name + " host ls"},
		{[]string{"host", "list"}, []string{"ls"}, brand.Name + " host ls"},
		{[]string{"completion", "bsh"}, []string{"bash", "zsh"}, brand.Name + " completion bash"},
		{[]string{"ls", "--jsn"}, []string{"--json"}, brand.Name + " ls --json"},
	} {
		cmd, err := line(t, c.args...)
		var u *usageError
		if !errors.As(err, &u) {
			t.Errorf("%s = %v, want a usage error", c.args, err)
			continue
		}
		if strings.Join(u.suggestions, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s suggests %q, want %q", c.args, u.suggestions, c.want)
		}
		if r := describe(err, cmd); r.try != c.try {
			t.Errorf("%s: Try: %q, want %q", c.args, r.try, c.try)
		}
	}
}

// The Try: line fixes the word that the parser found, where the user typed
// it, and not an earlier word that looks the same.
func TestTheTryLineFixesTheWordWhereItIs(t *testing.T) {
	hosttest.New(t)
	for _, c := range []struct {
		args []string
		try  string
	}{
		{[]string{"--debug-file", "atach", "atach", "box/s"}, brand.Name + " --debug-file atach attach box/s"},
		{[]string{"host", "--debug-file", "ad", "ad", "box"}, brand.Name + " host --debug-file ad add box"},
		{[]string{"new", "box", "--name", "--detatch", "--detatch"}, brand.Name + " new box --name --detatch --detach"},
		{[]string{"debug", "start", "box/s", "--onl=memory"}, brand.Name + " debug start box/s --only=memory"},
	} {
		cmd, err := line(t, c.args...)
		if r := describe(err, cmd); r.try != c.try {
			t.Errorf("%s: Try: %q, want %q", c.args, r.try, c.try)
		}
	}
}

func TestSynonymsNameRealCommands(t *testing.T) {
	root := Root()
	for parent, words := range synonyms {
		for typed, target := range words {
			path := append(strings.Fields(parent), strings.Fields(target)...)
			found, rest, err := root.Find(path)
			if err != nil || len(rest) != 0 || found == root || found.Hidden {
				t.Errorf("the synonym %q under %q names %q, which is not a command", typed, parent, target)
			}
		}
	}
}

// The acceptance lines of task C2 (docs/contributing/cli-plan.md), and the
// errors of names that do not exist.
func TestErrorsOfTheCommandLine(t *testing.T) {
	newTestHost(t)
	for _, c := range []struct {
		args []string
		code int
		has  []string
	}{
		{[]string{"atach", "x"}, 2, []string{`Did you mean "attach"?`, "\nTry: " + brand.Name + " attach x\n"}},
		{[]string{"attach"}, 2, []string{"attach needs <host>/<session>", "Usage:   " + brand.Name + " attach", "Example: " + brand.Name + " attach my-vps/auth-fix", "Try: " + brand.Name + " attach --help"}},
		{[]string{"kill"}, 2, []string{"Example: " + brand.Name + " kill my-vps/auth-fix"}},
		{[]string{"host", "ad", "box"}, 2, []string{`host has no command "ad"`, `Did you mean "add"?`, "Try: " + brand.Name + " host add box"}},
		{[]string{"agents", "instal", "claude"}, 2, []string{`agents has no command "instal"`, "Try: " + brand.Name + " agents install claude"}},
		{[]string{"doctor", "termnal"}, 2, []string{`Did you mean "terminal"?`, "Try: " + brand.Name + " doctor terminal"}},
		{[]string{"install", "claude"}, 2, []string{`Did you mean "agents install"?`, "Try: " + brand.Name + " agents install claude"}},
		{[]string{"remove", "box/s"}, 2, []string{`Did you mean "rm"?`, "Try: " + brand.Name + " rm box/s"}},
		{[]string{"atach", "x", "--debug"}, 2, []string{"Try: " + brand.Name + " attach x --debug"}},
		{[]string{"atach", "x", "--json"}, 2, []string{"Try: " + brand.Name + " attach --help"}},
		{[]string{"completion", "bsh"}, 2, []string{`completion has no command "bsh"`, `Did you mean one of these: "bash", "zsh"?`, "Try: " + brand.Name + " completion bash\n"}},
		{[]string{"completion", "bash", "extra"}, 2, []string{"completion bash takes no words, and got 1 word"}},
		{[]string{"new", "box", "--detatch"}, 2, []string{"new has no flag --detatch", `Did you mean "--detach"?`, "Try: " + brand.Name + " new box --detach"}},
		{[]string{"new", "--dir"}, 2, []string{"--dir needs a value", "Try: " + brand.Name + " new --help"}},
		{[]string{"rename", "a", "b", "c"}, 2, []string{"rename takes <host>/<session> <new-name>, and got 3 words"}},
		{[]string{"attach", "boxe/auth-fix"}, 3, []string{`no host named "boxe"`, `Did you mean "box"?`, "Try: " + brand.Name + " attach box/auth-fix"}},
		{[]string{"attach", "bx/auth-fix"}, 3, []string{`no host named "bx"`, "Try: " + brand.Name + " host add bx"}},
		{[]string{"new", "bo", "--name", "bo"}, 3, []string{`Did you mean "box"?`, "Try: " + brand.Name + " new box --name bo"}},
		{[]string{"new", "--dir", "bo/s", "bo", "--detach"}, 3, []string{`Did you mean "box"?`, "Try: " + brand.Name + " new --dir bo/s box --detach"}},
		{[]string{"new", "bo", "--detach", "--", "bo"}, 3, []string{"Try: " + brand.Name + " new box --detach -- bo"}},
		{[]string{"ls", "--host=boxe"}, 3, []string{"Try: " + brand.Name + " ls --host=box"}},
		{[]string{"ls", "--host", "boxe", "--host", "boxe"}, 3, []string{"Try: " + brand.Name + " ls --host boxe --host box"}},
		{[]string{"agents", "install", "--host", "cluade", "cluade"}, 3, []string{`Did you mean "claude"?`, "Try: " + brand.Name + " agents install --host cluade claude"}},
		{[]string{"agents", "install", "claude", "--host", "boxe"}, 3, []string{`no host named "boxe"`, "Try: " + brand.Name + " agents install claude --host box"}},
		{[]string{"help", "host", "ad"}, 2, []string{`host has no command "ad"`, `Did you mean "add"?`, "Try: " + brand.Name + " help host add"}},
		{[]string{"help", "atach"}, 2, []string{`unknown command "atach"`, "Try: " + brand.Name + " help attach"}},
		{[]string{"help", "-"}, 2, []string{`unknown command "-"`, "\nTry: " + brand.Name + " --help\n"}},
		{[]string{"help", "host", "--", "-bogus"}, 2, []string{`host has no command "-bogus"`, "\nTry: " + brand.Name + " host --help\n"}},
		{[]string{"setup", "--host", "box", "--agents", "cluade ,cluade", "--yes"}, 3, []string{`Did you mean "claude"?`, "Try: " + brand.Name + " setup --host box --agents 'claude ,cluade' --yes\n"}},
		{[]string{"setup", "--host", "box", "--agents", " cluade ", "--yes"}, 3, []string{`Did you mean "claude"?`, "Try: " + brand.Name + " setup --host box --agents ' claude ' --yes\n"}},
		{[]string{"attach", "missing;id/s"}, 3, []string{`no host named "missing;id"`, "\nTry: Name one of the hosts that " + brand.Name + " host ls lists.\n"}},
		{[]string{"new", "my host"}, 3, []string{`no host named "my host"`, "\nTry: Name one of the hosts that " + brand.Name + " host ls lists.\n"}},
		{[]string{"ls", "--host=-x"}, 3, []string{`no host named "-x"`, "\nTry: Name one of the hosts that " + brand.Name + " host ls lists.\n"}},
		{[]string{"setup", "--host", "dev@x;y", "--name", "box", "--yes"}, 1, []string{"there is already a host called box", "\nTry: " + brand.Name + " setup --host 'dev@x;y' --name <another-name>, or " + brand.Name + " setup --host box\n"}},
		{[]string{"debug", "start", "box/s", "--only", "net"}, 2, []string{"\nTry: Name attach, memory or runtime, and record net and launcher on this computer with --debug.\n"}},
		{[]string{"host", "rm", "bo"}, 3, []string{`Did you mean "box"?`, "Try: " + brand.Name + " host rm box"}},
		{[]string{"host", "rm", "nowhere"}, 3, []string{`no host named "nowhere"`, "Try: " + brand.Name + " host ls"}},
		{[]string{"agents", "install", "cluade"}, 3, []string{`Did you mean "claude"?`, "Try: " + brand.Name + " agents install claude"}},
		{[]string{"setup", "--host", "box", "--agents", "codex,cluade", "--yes"}, 3, []string{`Did you mean "claude"?`, "Try: " + brand.Name + " setup --host box --agents codex,claude --yes"}},
	} {
		got, code := lineErr(t, c.args...)
		if code != c.code {
			t.Errorf("%s exits with %d, want %d:\n%s", strings.Join(c.args, " "), code, c.code, got)
		}
		for _, h := range c.has {
			if !strings.Contains(got, h) {
				t.Errorf("%s printed\n%s\nwhich lacks %q", strings.Join(c.args, " "), got, h)
			}
		}
		if n := strings.Count(got, "Try: "); n != 1 {
			t.Errorf("%s printed %d Try: lines", strings.Join(c.args, " "), n)
		}
	}
	if got, code := lineErr(t, "host", "test", "nowhere"); code != 3 || !strings.Contains(got, "Try: "+brand.Name+" host add nowhere") {
		t.Errorf("host test nowhere = %d:\n%s", code, got)
	}
	if err := client.SaveHosts(nil); err != nil {
		t.Fatal(err)
	}
	if got, code := lineErr(t, "ls"); code != 1 || !strings.Contains(got, "no hosts yet") {
		t.Errorf("ls with no hosts = %d:\n%s", code, got)
	}
}

// A hosts.toml that does not decode is the user's to fix, and says where.
// One that cannot be read is a failure.
func TestBrokenHostsFile(t *testing.T) {
	hosttest.New(t)
	path := filepath.Join(client.ConfigDir(), "hosts.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for body, where := range map[string]string{
		"[[host]]\nname = \"box\"\nssh = \n":             "hosts.toml, line 3, column 7: expected value",
		"[[host]]\n  name = 1\n":                         "hosts.toml, line 2, column 3: incompatible types",
		"[[host]]\nname = \"box\"\nlabels = { a = 1 }\n": "hosts.toml, line 3, column 1: incompatible types",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got, code := lineErr(t, "host", "ls")
		if code != 2 || !strings.Contains(got, where) || !strings.Contains(got, "Try: Fix line ") {
			t.Errorf("%q gives %d:\n%s", body, code, got)
		}
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if got, code := lineErr(t, "host", "ls"); code != 1 || !strings.Contains(got, "permission denied") || !strings.Contains(got, "debug bundle") {
		t.Errorf("an unreadable hosts.toml gives %d:\n%s", code, got)
	}
}

// A host's codes exit with the kind of the 5.1 table, through Main, the
// same as the client's own checks of the same mistakes.
func TestHostCodesExitThroughMain(t *testing.T) {
	newTestHost(t)
	id := newSession(t, "s", "sleep", "60")
	old := os.Args
	defer func() { os.Args = old }()
	for _, c := range []struct {
		args []string
		code int
		has  string
	}{
		{[]string{"new", "box", "--agent", "nosuch", "--detach"}, 3, `unknown agent "nosuch"`},
		{[]string{"rename", "box/" + id, " "}, 2, "a name is needed"},
		{[]string{"new", "box", "--memory-max", "1M", "--detach"}, 2, "--memory-max"},
		{[]string{"new", "box", "--dir", "/no/such/folder", "--detach"}, 4, "does not exist"},
		{[]string{"host", "rm", "nowhere"}, 3, "no host named"},
		{[]string{"atach"}, 2, "unknown command"},
		{[]string{"config", "path"}, 0, ""},
	} {
		os.Args = append([]string{"client"}, c.args...)
		got, code := captureStderr(t, Main)
		if code != c.code || !strings.Contains(got, c.has) {
			t.Errorf("Main %q = %d, want %d:\n%s", c.args, code, c.code, got)
		}
	}
}

// A noun alone prints its help and exits with 0, and doctor alone runs.
func TestNounsAloneShowTheirHelp(t *testing.T) {
	hosttest.New(t)
	for _, args := range [][]string{{"host"}, {"agents"}, {"config"}, {"debug"}, {"completion"}} {
		out, err := run(t, args...)
		if err != nil || !strings.Contains(out, "Available Commands:") {
			t.Errorf("%s = %v:\n%s", args, err, out)
		}
	}
	if out, err := run(t, "doctor"); err != nil || !strings.Contains(out, "This computer:") {
		t.Errorf("doctor = %v:\n%s", err, out)
	}
}

// Each code that a host sends for a request that is otherwise right exits
// through Main with the kind of the 5.1 table.
func TestEachHostCodeExitsThroughMain(t *testing.T) {
	hosttest.New(t)
	_, tr := withFake(t, nil)
	if err := client.SaveHosts([]client.Host{{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}}); err != nil {
		t.Fatal(err)
	}
	old := os.Args
	defer func() { os.Args = old }()
	for code, want := range map[string]int{
		"unknown_agent": 3, "memory_max": 2, "bad_name": 2, "bad_secret": 2,
		"not_found": 3, "no_folder": 4, "bad_request": 1, "wall_floor": 5,
	} {
		tr.Fail("new", &proto.Error{Code: code, Message: "the host refused it", Hint: "Do something else."})
		os.Args = []string{"client", "new", "box", "--detach"}
		got, exit := captureStderr(t, Main)
		if exit != want || !strings.Contains(got, "Error: the host refused it.") || !strings.Contains(got, "Try: Do something else.") {
			t.Errorf("the host's %s exits with %d, want %d:\n%s", code, exit, want, got)
		}
	}
}

// A hint that repeats a word the user typed quotes it for the shell.
func TestHintsQuoteWhatTheUserTyped(t *testing.T) {
	newTestHost(t)
	newSession(t, "a b", "sleep", "60")
	got, code := lineErr(t, "forward", "box/a b", "x;y")
	if want := "\nTry: " + brand.Name + " forward 'box/a b' 3000\n"; code != 2 || !strings.Contains(got, want) {
		t.Errorf("forward = %d:\n%s\nwant %q", code, got, want)
	}
}
