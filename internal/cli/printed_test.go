package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Printed commands (docs/contributing/cli-spec.md 9.2): every command that
// the CLI prints for a person names a session by host/ID. Each one parses
// as the command that it names, and its address finds the same session,
// also when two names clean to the same text, when a name is another
// session's ID, and when a long host's name would cut the line.

// printedShellWords splits line as a shell does, for the words that shellWord
// makes: plain words, and words in single quotes, with '\” for a quote.
func printedShellWords(line string) []string {
	var out []string
	var w strings.Builder
	in, has := false, false
	for i := range len(line) {
		c := line[i]
		switch {
		case c == '\'':
			in, has = !in, true
		case c == ' ' && !in:
			if has {
				out = append(out, w.String())
			}
			w.Reset()
			has = false
		default:
			w.WriteByte(c)
			has = true
		}
	}
	if has {
		out = append(out, w.String())
	}
	return out
}

// commandsIn is each command in text, a hint such as "<brand> resume
// box/x, or <brand> rm box/x (removes it)", without its note in brackets.
func commandsIn(text string) []string {
	var out []string
	for _, part := range strings.Split(text, ", or ") {
		part = strings.TrimSpace(part)
		if i := strings.Index(part, " ("); i >= 0 {
			part = part[:i]
		}
		if strings.HasPrefix(part, brand.Name+" ") {
			out = append(out, part)
		}
	}
	return out
}

// tryOf is the Try: line of err, as the terminal shows it.
func tryOf(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("no error")
	}
	text, _ := reportOf(err)
	_, try, ok := strings.Cut(text, "Try: ")
	if !ok {
		t.Fatalf("no Try: line in %q", text)
	}
	return strings.TrimSpace(try)
}

// findsSession checks line, a command that the CLI printed for the session
// want of host: it is a command that runs, and its address finds want in
// list. A command that lists sessions, the stand-in for one that would not
// reach the terminal whole, passes only when long says so.
func findsSession(t *testing.T, what, line, host string, list []client.Session, want string, long bool) {
	t.Helper()
	words := printedShellWords(line)
	if len(words) < 2 || words[0] != brand.Name || !parses(words[1:]) {
		t.Errorf("%s: %q does not run", what, line)
		return
	}
	if words[1] == "ls" {
		if !long {
			t.Errorf("%s: %q names no session", what, line)
		}
		return
	}
	c, rest, err := Root().Find(words[1:])
	if err == nil {
		err = c.ParseFlags(rest)
	}
	if err != nil || len(c.Flags().Args()) == 0 {
		t.Errorf("%s: %q: %v", what, line, err)
		return
	}
	h, ref, err := client.SplitTarget(c.Flags().Args()[0])
	if err != nil || h != host {
		t.Errorf("%s: %q names the host %.20q, want %.20q: %v", what, line, h, host, err)
		return
	}
	if s, err := findIn(nil, list, ref, host); err != nil || s.ID != want {
		t.Errorf("%s: %.60q finds %+v, %v, want %s", what, line, s, err, want)
	}
}

// typed is the command of args, as the user typed them, for a Try: line
// that repeats them.
func typed(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	c, _, err := Root().Find(args)
	if err != nil {
		t.Fatal(err)
	}
	c.SetContext(withLine(context.Background(), args))
	return c
}

// trickyList is a host's sessions: two whose names clean to the same text,
// one whose name is another's ID, as an older helper allowed, one with no
// name, two with the same name, one whose ID, from a hostile host, a shell
// would split, and two with the IDs of older helpers.
func trickyList() []client.Session {
	return []client.Session{
		{ID: "a2a2a2a2", Name: "auth\u202e", Agent: "claude", State: "errored", ExitCode: new(1)},
		{ID: "b3b3b3b3", Name: "auth", Agent: "claude", State: "exited", ExitCode: new(0)},
		{ID: "c4c4c4c4", Name: "a2a2a2a2", Agent: "claude", State: "lost", Reason: "the host restarted"},
		{ID: "d5d5d5d5", Agent: "claude", State: "errored", OOM: true, Signal: "killed", ExitCode: new(137)},
		{ID: "e6e6e6e6", Name: "dup", Agent: "codex", State: "errored", ExitCode: new(2)},
		{ID: "f7f7f7f7", Name: "dup", Agent: "codex", State: "working"},
		{ID: "g7 x;y", Name: "odd", Agent: "codex", State: "errored", ExitCode: new(1)},
		// IDs of older helpers, without a digit, or shorter.
		{ID: "oldoldid", Name: "old", Agent: "codex", State: "errored", ExitCode: new(1)},
		{ID: "f6", Name: "short", Agent: "codex", State: "exited", ExitCode: new(0)},
	}
}

// Every printed command of a resolved session, from every place that
// prints one, finds that session again: on a short host, on a host with a
// name of 60 characters, and on a host whose name of 3990 characters
// would cut the line. There, and for an ID that a shell would split, a
// command that lists sessions stands in.
func TestPrintedCommandsFindTheirSession(t *testing.T) {
	list := trickyList()
	for _, host := range []string{"box", strings.Repeat("h", 60), strings.Repeat("h", 3990)} {
		for _, s := range list {
			long := len(host) > 100 || client.ShellWord(host+"/"+s.ID) != host+"/"+s.ID
			what := func(where string) string {
				return fmt.Sprintf("%s of %s on a host of %d characters", where, s.ID, len(host))
			}
			check := func(where, line string) { findsSession(t, what(where), line, host, list, s.ID, long) }
			// show's attach line.
			for _, l := range strings.Split(describeSession(host, s), "\n") {
				if rest, ok := strings.CutPrefix(strings.TrimSpace(l), "Attach"); ok {
					check("show", strings.TrimSpace(rest))
				}
			}
			// The error of an ended agent, and the end of an attach.
			for _, c := range commandsIn(tryOf(t, endedError(client.Host{Name: host}, s))) {
				if !strings.HasPrefix(c, brand.Name+" host ") {
					check("the ended error", c)
				}
			}
			if msg := endMessage(s.Name, host, s.ID, &proto.Ended{OOM: true, ExitCode: 137}); strings.Contains(msg, "Try: ") {
				_, try, _ := strings.Cut(msg, "Try: ")
				check("the end of an attach", commandsIn(shown(try))[0])
			} else if !long {
				t.Errorf("the end of an attach of %s has no step: %q", s.ID, msg)
			}
			// The questions, with input off, after the user typed the name.
			for _, args := range [][]string{{"attach", s.Name, "--no-input"}, {"attach", "--no-input"}, {"flip", s.Name}} {
				_, err := askRestart(sessionPrompts(typed(t, args...), host, s.ID), host, s)
				check("attach's question after "+strings.Join(args, " "), tryOf(t, err))
			}
			for _, args := range [][]string{{"rm", s.Name}, {"rm"}, {"--debug-file", strings.Repeat("x", 3990), "rm", s.Name}} {
				_, err := askDelete(sessionPrompts(typed(t, args...), host, s.ID), host, s)
				check("rm's question after "+args[0], tryOf(t, err))
			}
			// rm without a host, when a host did not answer.
			for _, unanswered := range [][]string{{"nuc"}, {host}} {
				m := match{host: client.Host{Name: host}, s: s}
				check("rm's unsure match", tryOf(t, unsureMatch(typed(t, "rm", s.Name, "-y"), s.Name, m, unanswered)))
				check("kill's unsure match", tryOf(t, unsureMatch(typed(t, "kill", s.Name), s.Name, m, unanswered)))
			}
			// A word that names two sessions: the first is the one tried.
			other := match{host: client.Host{Name: "nuc"}, s: client.Session{ID: "z7z7z7z7", Name: s.Name}}
			check("an ambiguous word", tryOf(t, ambiguousMatches(typed(t, "attach", s.Name), s.Name, []match{{host: client.Host{Name: host}, s: s}, other})))
			// The notes and the hints of commands that found it.
			check("the debug command", sessionCommand(host, s.ID, "debug start"))
			check("forward's hint", sessionCommand(host, s.ID, "forward", "3000"))
			check("forward's error", tryOf(t, notAPort(host, &s, "x;y")))
			errOut, _ := said(t, func() error {
				debugStopped(nil, client.Host{Name: host}, &client.ResumeResult{DebugStopped: true, Session: s})
				return nil
			})
			_, again, ok := strings.Cut(errOut, "To record again: ")
			if !ok {
				t.Fatalf("resume's debug note: %q", errOut)
			}
			check("resume's debug note", strings.TrimSpace(again))
		}
		// A name that two sessions share, on the host's own list.
		_, err := findIn(typed(t, "rm", "dup", "-y"), list, "dup", host)
		findsSession(t, "a shared name", tryOf(t, err), host, list, "e6e6e6e6", len(host) > 100)
	}
}

// forward's error for a word that is not a port names the session by
// host/ID, from forwardPairs itself: a line that runs, or the list of the
// host's sessions when the ID could not go in a command.
func TestForwardsErrorOfAWordThatIsNoPort(t *testing.T) {
	old := sessionPorts
	t.Cleanup(func() { sessionPorts = old })
	sessionPorts = func(context.Context, *client.Conn, string) ([]client.Port, error) {
		return []client.Port{{Port: 3000, Address: "127.0.0.1"}}, nil
	}
	list := trickyList()
	for _, s := range list {
		_, err := forwardPairs(context.Background(), nil, client.Host{Name: "box"}, &s, []string{"x;y"})
		long := client.ShellWord("box/"+s.ID) != "box/"+s.ID
		findsSession(t, "forward's error of "+s.ID, tryOf(t, err), "box", list, s.ID, long)
		if try := tryOf(t, err); long && try != brand.Name+" ls box" {
			t.Errorf("forward's error of %q: Try: %q", s.ID, try)
		}
	}
}

// A Try: line runs as written: when a word of the line that the user typed
// would not reach the terminal whole, the line is the command and the
// session's address alone.
func TestATryLineRunsAsWritten(t *testing.T) {
	for args, want := range map[[4]string]string{
		{"rm", "a", "--debug-file", "x\u202ey"}: brand.Name + " rm box/abcd2345",
		{"rm", "a", "--debug-file", "x-y"}:      brand.Name + " rm box/abcd2345 --debug-file x-y",
		{"kill", "a", "--debug-file", "x\ty"}:   brand.Name + " rm -y box/abcd2345",
	} {
		if got := tryAddress(typed(t, args[:]...), "box", "abcd2345"); got != want {
			t.Errorf("%q: Try: %q, want %q", args, got, want)
		}
	}
}

// A parser's suggestion, for a misspelt command or flag or an unknown host,
// repeats the line that the user typed with one word fixed (cli-spec 4.3).
// It names a session only by host/ID, as typed, and otherwise lists the
// sessions of the host: no lookup found the word, and its name may mean
// another session. Sessions A and B have names that clean to the same
// text, so A's name, cleaned, is B's.
func TestParserSuggestionsNameNoOtherSession(t *testing.T) {
	newTestHost(t)
	list := trickyList()
	a, aName, bName := "a2a2a2a2", "auth\u202e", "auth"
	for _, args := range [][]string{
		{"rn", "box/" + aName, "-y"}, {"rn", "box/" + bName, "-y"}, {"rn", "box/" + a, "-y"},
		{"rm", "box/" + aName, "--yse"}, {"rm", "box/" + bName, "--yse"}, {"rm", "box/" + a, "--yse"},
		{"kil", "box/" + aName}, {"kil", "box/" + bName}, {"kil", "box/" + a},
		{"flip", "box/" + aName, "--restrt"}, {"flip", "box/" + bName, "--restrt"}, {"flip", "box/" + a, "--restrt"},
		{"fli", "box/" + bName}, {"fli", "box/" + a},
		{"rm", "boxe/" + aName, "-y"}, {"rm", "boxe/" + bName, "-y"}, {"rm", "boxe/" + a, "-y"},
		{"kill", "boxe/" + bName}, {"kill", "boxe/" + a},
		// The reviewer's probe (review 5, finding 1), and --json without
		// --detach, whose Try: line repeats the typed line too.
		{"rm", aName, "--yess"}, {"rm", bName, "--yess"}, {"rmm", "box/" + aName, "-y"},
		{"resume", "box/" + aName, "--json"}, {"resume", "box/" + bName, "--json"}, {"resume", "box/" + a, "--json"},
		{"new", "box", "--json"},
	} {
		got, _ := lineErr(t, args...)
		_, try, _ := strings.Cut(got, "Try: ")
		try = strings.TrimSpace(try)
		words := printedShellWords(try)
		if strings.HasSuffix(try, " --help") || len(words) >= 2 && words[1] == "ls" {
			continue
		}
		c, rest, err := Root().Find(words[1:])
		if err == nil && !strings.Contains(c.Use, "<host>/<session>") {
			continue // a command that takes no session, such as host add
		}
		if err == nil {
			err = c.ParseFlags(rest)
		}
		if err != nil || len(c.Flags().Args()) == 0 || c.Flags().Args()[0] != "box/"+a {
			t.Errorf("%q: Try: %q names a session other than by host/ID: %v", args, try, err)
			continue
		}
		findsSession(t, strings.Join(args, " "), try, "box", list, a, false)
	}
}

// A question's Try: line repeats the typed line only when each word of it
// reaches the terminal whole: otherwise it is the command's help.
func TestAQuestionsLineRunsAsWritten(t *testing.T) {
	var ue *client.UserError
	for args, want := range map[[4]string]string{
		{"new", "box", "--dir", "x\u202ey"}: brand.Name + " new --help",
		{"new", "box", "--dir", "x-y"}:      brand.Name + " new box --dir x-y --mkdir",
	} {
		if err := prompts(typed(t, args[:]...)).needs(qMakeFolder, "a yes"); !errors.As(err, &ue) || ue.Hint != want {
			t.Errorf("%q: %v, want the Try: line %q", args, err, want)
		}
	}
}

// A word that names no session gets a Try: line that lists the sessions,
// and suggestions by host/ID, each with its name beside it. Neither
// repeats the word as a command.
func TestAnUnknownWordIsNeverACommand(t *testing.T) {
	list := trickyList()
	_, err := findIn(typed(t, "rm", "authx", "-y"), list, "authx", "box")
	text, code := reportOf(err)
	if code != 3 || !strings.Contains(text, "Try: "+brand.Name+" ls box\n") || strings.Contains(text, "rm authx") {
		t.Errorf("an unknown word: exit %d\n%s", code, text)
	}
	var u *usageError
	if !errors.As(err, &u) || len(u.suggestions) == 0 {
		t.Fatalf("no suggestions: %v", err)
	}
	for i, addr := range u.suggestions {
		findsSession(t, "a suggestion", brand.Name+" attach "+addr, "box", list, strings.TrimPrefix(addr, "box/"), false)
		if !strings.Contains(text, "\""+addr+"\" ("+shown(u.labels[i])+")") {
			t.Errorf("the suggestion %s is not shown with its name:\n%s", addr, text)
		}
	}
	// attach without a terminal repeats only an address that means one
	// session as it is.
	for word, want := range map[string]string{
		"box/a2a2a2a2": brand.Name + " send box/a2a2a2a2 'text'",
		"-":            brand.Name + " send - 'text'",
		"auth\u202e":   brand.Name + " send <host>/<ID> 'text'",
		"box/auth":     brand.Name + " send <host>/<ID> 'text'",
		"b x/a2a2a2a2": brand.Name + " send <host>/<ID> 'text'",
	} {
		err := runAttach(typed(t, "attach", word), []string{word}, false)
		if got := tryOf(t, err); got != want {
			t.Errorf("attach %q without a terminal: Try: %q, want %q", word, got, want)
		}
	}
}
