package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Ended sessions (docs/contributing/ux-spec.md 2.4). A session's holder
// outlives its agent, so Alive says only that the holder answers. Whatever
// needs the agent itself (attach, the activity line, the memory) asks
// ended instead. Whatever needs only the holder (the last screen) asks
// Alive. For an ended session, the row says how the agent ended, and the
// line under the list says why and what to do next.

// agentEnded is why a session whose agent has ended cannot be attached.
const agentEnded = "the agent has ended"

// ended reports whether the session's agent has ended (client.Ended).
func ended(s client.Session) bool { return client.Ended(s) }

// endLook is how an ended session's state is drawn. The host marks every
// non-zero exit "errored", so the label comes from the out-of-memory mark,
// the signal and the exit code. Every label fits in colState.
func endLook(s client.Session) (glyph, label seg) {
	fail := func(l string) (seg, seg) { return seg{glyphFail, sDanger}, seg{l, sDanger} }
	switch {
	case s.State == api.StateLost:
		return seg{glyphOffline, sFaint}, seg{"lost", sFaint}
	case client.CapStopped(s):
		return fail("out of memory")
	case s.Signal != "":
		return fail(signalWord(s.Signal))
	case s.ExitCode != nil && *s.ExitCode != 0:
		return fail(fmt.Sprintf("exited %d", *s.ExitCode))
	case s.ExitCode != nil:
		return seg{glyphOK, sSuccess}, seg{"exited 0", sFaint}
	case s.State == api.StateErrored:
		return fail("failed")
	default:
		return seg{glyphOK, sSuccess}, seg{"exited", sFaint}
	}
}

// signalWords are short words for the signals that end an agent, by the
// name the host sends: Go's name for the signal, such as "terminated".
var signalWords = map[string]string{
	"terminated":               "terminated",
	"interrupt":                "interrupted",
	"hangup":                   "hung up",
	"segmentation fault":       "crashed",
	"bus error":                "crashed",
	"illegal instruction":      "crashed",
	"floating point exception": "crashed",
	"aborted":                  "crashed",
	"trace/breakpoint trap":    "crashed",
	"bad system call":          "crashed",
}

// signalWord is the label of an agent that a signal ended: "killed", or
// "stopped" for a signal without a word of its own.
func signalWord(sig string) string {
	if client.IsKill(sig) {
		return "killed"
	}
	if w, ok := signalWords[strings.ToLower(sig)]; ok {
		return w
	}
	return "stopped"
}

// endWhy says why an ended session's agent ended, for the line under the
// list: "stopped at its memory cap of 1.5 GiB".
func endWhy(s client.Session) string {
	if s.State == api.StateLost {
		if s.Reason != "" {
			return s.Reason
		}
		return "its holder stopped"
	}
	reason := s.Reason
	if s.OOM || reason == "oom" || reason == api.ReasonChildOOM {
		// The memory cap's mark and texts. The cases below say when the cap
		// stopped the agent, and the note at the right of the line says when
		// it stopped another process. Other reasons, such as why the agent
		// did not start, stay.
		reason = ""
	}
	switch {
	case client.CapStopped(s) && s.MemoryMax > 0:
		return "stopped at its memory cap of " + fmtSize(s.MemoryMax)
	case client.CapStopped(s):
		return "stopped at its memory cap"
	case s.Signal != "" && s.ExitCode != nil && *s.ExitCode > 128:
		return fmt.Sprintf("stopped by signal %d (%s)", *s.ExitCode-128, s.Signal)
	case s.Signal != "":
		return "stopped by a signal (" + s.Signal + ")"
	case s.ExitCode != nil && reason != "":
		return fmt.Sprintf("exited with code %d: %s", *s.ExitCode, reason)
	case s.ExitCode != nil:
		return fmt.Sprintf("exited with code %d", *s.ExitCode)
	case reason != "":
		return reason
	default:
		return "ended"
	}
}

// endHint is the step to try after an agent ended as e says, as the
// launcher shows it, or "". After the memory cap stopped the agent, it is
// the key that resumes it, and the command for a bigger cap. Only the
// command line can give one. It says what client.EndHint says on the
// command line. It fits one line of the notices at 60 columns, so the end
// and its step both show.
func endHint(e *proto.Ended) string {
	if e == nil || !e.OOM {
		return ""
	}
	return resumeKey + " resumes it, or run " + brand.Name + " resume --memory-max."
}

// resumeKey is the key that resumes a session whose agent has ended: a
// letter of "resume". r renames and s opens the guided setup, and e would
// sit too close to alt+e, which shows ended sessions. So it is u.
const resumeKey = "u"

// endedSteps are the next steps for a session whose agent has ended, the
// most useful first. They show in the line under the list, and the ones
// that are keys start the keys at the bottom. A step that the launcher has
// no key for is a command, after a cyan arrow. The resume brings the agent
// back in the same session. It goes first, except after a clean exit, as
// when the user quit the agent, where a delete is more likely.
func endedSteps(h *hostConn, s client.Session) []hint {
	resume, del := hint{resumeKey, "resume"}, hint{"d", "delete"}
	switch {
	case s.State == api.StateLost:
		// The resume acts on this session, with one key. It also fetches
		// the session's secrets again, which a restore cannot, and it works
		// after restore has used its tries. The restore command brings back
		// every session that a host restart stopped, so it follows. A
		// delete would remove the session's ID, which both need.
		return []hint{resume, {glyphNext, restoreCommand(h)}, del}
	case client.CleanExit(s):
		return []hint{del, resume}
	default:
		return []hint{resume, del}
	}
}

// restoreCommand brings back the sessions on h that lost their holder.
func restoreCommand(h *hostConn) string {
	return fmt.Sprintf("%s host restore %s --all", brand.Name, h.name())
}

// stepText draws one next step: a key, as the keys at the bottom show it,
// or a command after a cyan arrow.
func stepText(st hint) string {
	if st.key == glyphNext {
		return sAccent.Render(glyphNext) + " " + sLink.Render(st.desc)
	}
	return sKey.Render(st.key) + sFaint.Render(" "+st.desc)
}

// endedHints are the keys of the sessions list while an ended session is
// selected: its next steps that are keys, then what still works. Enter
// goes forward to the agent's last screen, which needs the holder. For a
// lost agent, Enter shows the restore command whole, in a notice, so its
// key takes the command's place among the steps.
func endedHints(h *hostConn, s client.Session) []hint {
	var hs []hint
	for _, st := range endedSteps(h, s) {
		switch {
		case st.key != glyphNext:
			hs = append(hs, st)
		case s.State == api.StateLost:
			hs = append(hs, hint{"enter", "how to restore"})
		}
	}
	if s.Alive {
		hs = append(hs, hint{"enter", "peek"})
	}
	// Rename still works, but it is rare on an agent that has ended, so it
	// gives its place to the commands.
	return append(hs, hint{"esc", "hosts"}, hint{"ctrl+o", "commands"}, hint{"?", "keys"})
}

// enterEnded is the Enter key on a session whose agent has ended, where
// the attach command cannot run. It reports whether it took the key.
func (m *Model) enterEnded() (tea.Cmd, bool) {
	h, s := m.selectedPair()
	if s == nil || !ended(*s) {
		return nil, false
	}
	return m.openEnded(h, *s), true
}

// openEnded is Enter on a session whose agent has ended. While the holder
// answers, Enter goes forward to the agent's last screen. Otherwise a
// notice says why there is none, and the step to try. The notice reads like
// the one at the end of an attach (onAttachDone), with the same step after
// the memory cap (endHint).
func (m *Model) openEnded(h *hostConn, s client.Session) tea.Cmd {
	if s.Alive {
		return m.openPreview()
	}
	text := fmt.Sprintf("%s on %s ended", s.Name, h.name())
	if c := endCause(s); c != "" {
		text += " (" + c + ")"
	}
	hint := endHint(&proto.Ended{OOM: client.CapStopped(s)})
	switch {
	case hint != "":
	case s.State == api.StateLost:
		// The command whole, which the line under the list may cut.
		hint = restoreCommand(h)
	default:
		text += ", and its holder has stopped"
		hint = "Press " + resumeKey + " to resume it, or d to delete it."
	}
	return m.toastNext(toastWarning, text+".", hint)
}

// endCause is why an agent ended, in the words of the notice at the end of
// an attach: "out of memory", the signal, "exit 1", or why its holder was
// lost. It is "" when the session does not say.
func endCause(s client.Session) string {
	switch {
	case s.State == api.StateLost:
		return endWhy(s)
	case client.CapStopped(s):
		return "out of memory"
	case s.Signal != "":
		return s.Signal
	case s.ExitCode != nil:
		return fmt.Sprintf("exit %d", *s.ExitCode)
	default:
		return ""
	}
}
