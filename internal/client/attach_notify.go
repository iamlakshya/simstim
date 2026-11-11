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
		a.notify(body)
	}
}

// notify raises a system notification (OSC 777). It draws nothing on the
// screen, so the session keeps every row and column.
func (a *attacher) notify(msg string) {
	title := brand.DisplayName + " · " + a.o.Host.Name
	a.write([]byte("\x1b]777;notify;" + OSCText(title) + ";" + OSCText(msg) + "\x1b\\"))
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
