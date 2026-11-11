package client

import (
	"fmt"

	"github.com/iamlakshya/simstim/internal/brand"
)

// notifyOthers shows a desktop notification, through the local terminal, when
// another session on this host starts to need the user or ends.
func (a *attacher) notifyOthers(list []Session) {
	if a.seen == nil {
		a.seen = map[string]string{}
		for _, s := range list {
			a.seen[s.ID] = s.State
		}
		return
	}
	for _, s := range list {
		before, known := a.seen[s.ID]
		a.seen[s.ID] = s.State
		if s.ID == a.o.Session || !known || before == s.State {
			continue
		}
		var body string
		switch s.State {
		case "needs_input":
			body = s.Name + " needs you"
			if s.Reason != "" {
				body += ": " + s.Reason
			}
		case "exited", "errored":
			body = s.Name + " ended"
		default:
			continue
		}
		title := brand.DisplayName + " · " + a.o.Host.Name
		a.notice([]byte("\x1b]777;notify;"+OSCText(title)+";"+OSCText(body)+"\x1b\\"), false)
	}
}

// notify raises a system notification (OSC 777) about something that the
// user did, such as an upload. It draws nothing on the screen, so the
// session keeps every row and column. No such notice is ever dropped. Its
// text is the client's own, and names every file of a drop, so it is
// cleaned but not cut. Text from the host in it is cut where it enters.
func (a *attacher) notify(msg string) {
	title := brand.DisplayName + " · " + a.o.Host.Name
	a.notice([]byte("\x1b]777;notify;"+OSCText(title)+";"+oscText(msg, len(msg))+"\x1b\\"), true)
}

// maxBackground is how many notices about other sessions wait at most for
// the terminal. The others are dropped.
const maxBackground = 8

// heldNotice is a notice that waits for the terminal.
type heldNotice struct {
	seq  []byte
	keep bool // a notice of the user's own action, which is never dropped
}

// notice writes a notice when the terminal is between sequences and
// characters. Otherwise it holds the notice until the session's output gets
// there, so the notice never cuts a sequence of the session's.
func (a *attacher) notice(seq []byte, keep bool) {
	if a.guard.idle() {
		a.write(seq)
		return
	}
	if !keep {
		n := 0
		for _, h := range a.held {
			if !h.keep {
				n++
			}
		}
		if n >= maxBackground {
			return
		}
	}
	a.held = append(a.held, heldNotice{seq: seq, keep: keep})
}

// writeHeld writes the notices that waited, once the terminal is between
// sequences.
func (a *attacher) writeHeld() {
	if len(a.held) == 0 || !a.guard.idle() {
		return
	}
	held := a.held
	a.held = nil
	for _, n := range held {
		a.write(n.seq)
	}
}

// endGuard runs at detach, after the reset of the session's modes. It ends
// an iTerm2 copy that the session left running, which records it. A kitty
// transfer that has not ended counts as a write as far as it went, and its
// state waits for the next attach in this process. Then it writes the
// notices that still wait, and waits for the clipboard writes to reach the
// shared file.
func (a *attacher) endGuard() {
	if a.guard == nil {
		return
	}
	if a.guard.copying() {
		a.write([]byte("\x1b]1337;EndCopy\a"))
	}
	a.guard.detach()
	for _, n := range a.held {
		a.write(n.seq)
	}
	a.held = nil
	a.guard.mem.flush(guardFlushWait)
}

// flash shows the reconnect notice on the bottom row: the one thing the
// client draws while attached, and only while the link is down.
func (a *attacher) flash(msg string) {
	_, rows, _, _ := a.size()
	a.write([]byte(fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[2K\x1b[2m %s · %s\x1b[0m\x1b8", rows, brand.DisplayName, msg)))
}

func (a *attacher) clearFlash() {
	_, rows, _, _ := a.size()
	a.write([]byte(fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[2K\x1b8", rows)))
}
