//go:build unix

package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The commands that a command prints for the user to run next name the
// session by its ID. A name is the host's text, and could be shell syntax
// that would run on the laptop when the command is pasted.
func TestPrintedCommandsNameTheID(t *testing.T) {
	newTestHost(t)
	const name = "$(touch${IFS}owned)`id`;a && b | c\nd"
	inert := func(what, cmd string) {
		t.Helper()
		if strings.ContainsAny(cmd, "$`;&|\n") || strings.Contains(cmd, "touch") {
			t.Errorf("%s holds the session's name: %q", what, cmd)
		}
	}
	id := newSession(t, name, "/bin/sh", "-c", "exec sleep 60")
	out := okSaid(t, "debug", "start", "box/"+id, "--for", "10m")
	stop := out[strings.LastIndex(out, "To stop: "):]
	if stop != "To stop: "+brand.Name+" debug stop box/"+id+"\n" {
		t.Errorf("debug start = %q", stop)
	}
	if out := okSaid(t, "resume", "box/"+id, "--detach"); out != shown(name)+" on box is already running.\n" {
		t.Fatalf("resume of a running agent = %q", out)
	}
	// An already-running detached resume has no next-step error. The
	// describing command prints the live session's attach command instead.
	var attachLine string
	for _, line := range strings.Split(ok(t, "show", "box/"+id), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Attach"); ok {
			attachLine = strings.TrimSpace(rest)
		}
	}
	if attachLine != brand.Name+" attach box/"+id {
		t.Fatalf("show's attach command = %q", attachLine)
	}
	inert("show's attach command", attachLine)

	ended := newSession(t, name, "/bin/sh", "-c", "sleep 1; exit 2")
	agentEnded(t, ended)
	t.Setenv(noInputEnv, "")
	term, done := inTerminal(t, "attach", "box/"+ended)
	term.WaitFor("Start shell again in ")
	term.Type("n\r")
	err := wait(t, done)
	var ue *client.UserError
	if !errors.As(err, &ue) || ue.Kind != client.KindCancelled || !strings.HasPrefix(ue.Hint, brand.Name+" resume box/"+ended) {
		t.Fatalf("attach to an ended agent = %v", err)
	}
	inert("attach's step", ue.Hint)
	if got := endMessage(name, "box", ended, &proto.Ended{ExitCode: 137, OOM: true}); !strings.Contains(got, "Try: "+brand.Name+" resume box/"+ended+" ") {
		t.Errorf("the end message's step = %q", got)
	}
}
