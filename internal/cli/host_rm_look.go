package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// What host rm sees on a machine before it deletes it
// (docs/contributing/cli-spec.md 11.3, rules 3 and 9).

// machineLook is what host rm saw on a machine: its state, and its
// sessions when it runs.
type machineLook struct {
	state    string
	stateErr error
	sessions []client.Session
	listErr  error
}

// lookAtState asks the provider for the machine's state only.
func lookAtState(ctx context.Context, lc client.Lifecycle) machineLook {
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var l machineLook
	l.state, l.stateErr = lc.Status(sctx)
	return l
}

// lookAtMachine asks the provider for the machine's state, and a running
// machine for its sessions. It neither wakes the machine nor installs or
// prunes a helper: a missing helper is a list that failed.
func lookAtMachine(ctx context.Context, h client.Host, lc client.Lifecycle) machineLook {
	l := lookAtState(ctx, lc)
	if l.stateErr != nil || l.state != client.MachineRunning {
		return l
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := client.Dial(client.WithoutInstall(client.WithoutWake(sctx)), h, true)
	if err != nil {
		l.listErr = err
		return l
	}
	defer conn.Close()
	l.sessions, l.listErr = conn.Sessions(sctx)
	return l
}

// sessionNameMax is the most runes of a session's name that host rm
// shows.
const sessionNameMax = 100

// working names the sessions that work on the machine. The host sends the
// names, so they are cleaned and cut short before they reach the
// terminal.
func (l machineLook) working() []string {
	var out []string
	for _, s := range l.sessions {
		if s.Alive && (s.State == api.StateWorking || s.State == api.StateStarting) {
			out = append(out, api.CleanText(s.Name, sessionNameMax))
		}
	}
	sort.Strings(out)
	return out
}

// holds says what the machine holds, for the question.
func (l machineLook) holds() string {
	switch {
	case l.stateErr != nil:
		return "Its state is not known: " + shown(l.stateErr.Error()) + "."
	case l.state != client.MachineRunning:
		return "It is " + shown(l.state) + "."
	case l.listErr != nil:
		return "Its sessions could not be listed: " + shown(l.listErr.Error()) + "."
	}
	s := "It holds " + client.Plural(len(l.sessions), "session", "sessions") + "."
	if len(l.sessions) == 0 {
		s = "It holds no sessions."
	}
	if w := l.working(); len(w) > 0 {
		s += fmt.Sprintf(" %d %s working: %s.", len(w), isAre(len(w)), strings.Join(w, ", "))
	}
	return s
}

// isAre is the verb for n things: "is" or "are".
func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// refusal is the error of --destroy while a session works (cli-spec 11.3,
// rule 9), or nil. A running machine whose sessions could not be listed
// counts as one where a session works, and so does a machine whose state
// is not known.
func (m *rmMachine) refusal(o rmOpts) error {
	hint := rmOpts{name: m.h.Name, destroy: true, force: true, confirmed: o.confirmed}.line() + " (stops them too)"
	var msg string
	switch {
	case m.stateErr != nil:
		msg = fmt.Sprintf("the state of %s is not known, so a session may be working on it: %s", m.what(), m.stateErr)
	case m.listErr != nil:
		msg = fmt.Sprintf("%s runs, but its sessions could not be listed, so one may be working: %s", m.what(), m.listErr)
	default:
		w := m.working()
		if len(w) == 0 {
			return nil
		}
		msg = fmt.Sprintf("%s on %s %s working: %s", client.Plural(len(w), "session", "sessions"), m.h.Name, isAre(len(w)), strings.Join(w, ", "))
	}
	return &client.UserError{Msg: msg, Hint: hint, Code: "sessions_working"}
}
