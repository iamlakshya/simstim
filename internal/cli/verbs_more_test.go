package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// verbHostile holds the controls that a host could put in a session's words:
// a full reset (RIS), a clipboard write (OSC 52), a bell and a bidi
// override.
const verbHostile = "\x1bc\x1b]52;c;aGk=\x07\u202e"

// clean reports whether s holds none of verbHostile's controls.
func clean(s string) bool { return !strings.ContainsAny(s, "\x1b\x07\u202e") }

// The questions of rm and attach, and the text of show, clean every word
// that a host sends (major X3).
func TestHostWordsAreCleaned(t *testing.T) {
	s := client.Session{ID: "1a2b" + verbHostile, Name: "auth" + verbHostile, Agent: "claude" + verbHostile, Cwd: "/srv/app" + verbHostile,
		State: "errored", Source: "process" + verbHostile, Reason: "it failed" + verbHostile, Signal: "killed" + verbHostile, ExitCode: new(1),
		HostVersion: "0.2.0" + verbHostile, Unit: "unit" + verbHostile, Secrets: []string{"TOKEN" + verbHostile}, MissingSecrets: []string{"KEY" + verbHostile}}
	running := s
	running.State = "working"
	for name, ask := range map[string]func(p *prompter) (bool, error){
		"rm":                     func(p *prompter) (bool, error) { return askDelete(p, "box", running) },
		"rm, an ended agent":     func(p *prompter) (bool, error) { return askDelete(p, "box", s) },
		"attach, an ended agent": func(p *prompter) (bool, error) { return askRestart(p, "box", s) },
	} {
		var out bytes.Buffer
		if _, err := ask(testPrompts("n\n", &out)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !clean(out.String()) || !strings.Contains(out.String(), "auth") && !strings.Contains(out.String(), "claude") {
			t.Errorf("%s asked %q", name, out.String())
		}
	}
	if got := describeSession("box", s); !clean(got) || !strings.Contains(got, "it failed") {
		t.Errorf("show printed %q", got)
	}
	// The question's text when input is off, as the error shows it.
	p := quietPrompts(context.Background())
	_, err := askDelete(p, "box", running)
	if got, _ := reportOf(err); !clean(got) {
		t.Errorf("the error printed %q", got)
	}
}

// kill without a session warns too, and teaches rm: its missing session is
// rm's, with rm's usage and example.
func TestKillWithoutASession(t *testing.T) {
	newTestHome(t)
	out, errOut, code := mainRun(t, "kill")
	if code != 2 || out != "" || strings.Count(errOut, `Warning: "kill" is now "rm".`) != 1 ||
		!strings.Contains(errOut, "Error: rm needs a session, as <host>/<session>.") || !strings.Contains(errOut, "Example: "+brand.Name+" rm my-vps/auth-fix") {
		t.Errorf("kill: exit %d\n%s", code, errOut)
	}
}
