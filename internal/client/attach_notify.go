package client

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

const maxPendingNotify = 16

// outStream tracks both terminal readings and a partial UTF-8 character.
// Only a ground boundary in both readings may carry a client notification.
// The translator receives at most one character, never an output frame.
type outStream struct {
	raw   uint8
	xterm uint8
	c1    c1Translator
}

func (s *outStream) reset() { *s = outStream{} }

func (s *outStream) ground() bool {
	return s.raw == vtGround && s.xterm == vtGround && len(s.c1.pend) == 0
}

func (s *outStream) step(c byte) {
	s.raw = vtStep(&vtNext, s.raw, c)
	s.stepX(c)
}

func (s *outStream) stepX(c byte) {
	if c < 0x80 && len(s.c1.pend) == 0 {
		s.xterm = vtStep(&vtNextX, s.xterm, c)
		return
	}
	one := [1]byte{c}
	for _, translated := range s.c1.translate(one[:]) {
		s.xterm = vtStep(&vtNextX, s.xterm, translated)
	}
}

// feed advances a whole frame when no notification needs an exact boundary.
func (s *outStream) feed(b []byte) { s.feedChecked(b, false) }

// feedChecked is feed for a frame that the caller may have checked: free
// says that c1Free(b) holds, and false says nothing.
func (s *outStream) feedChecked(b []byte, free bool) {
	s.raw = notifyAdvance(&vtNext, s.raw, b)
	if free && len(s.c1.pend) == 0 {
		s.xterm = notifyAdvance(&vtNextX, s.xterm, b)
		return
	}
	for len(b) > 0 {
		if len(s.c1.pend) > 0 {
			s.stepX(b[0])
			b = b[1:]
			continue
		}
		i := c1Index(b)
		if i < 0 {
			s.xterm = notifyAdvance(&vtNextX, s.xterm, b)
			return
		}
		s.xterm = notifyAdvance(&vtNextX, s.xterm, b[:i])
		b = b[i:]
		_, n, complete := c1At(b)
		if !complete {
			_ = s.c1.translate(b)
			return
		}
		s.xterm = notifyAdvance(&vtNextX, s.xterm, s.c1.translate(b[:n]))
		b = b[n:]
	}
}

// notifyAdvance needs only final state, not tokens or payloads. ESC resets
// every state, so bytes before the last ESC cannot affect that final state.
func notifyAdvance(table vtTable, state uint8, b []byte) uint8 {
	if last := bytes.LastIndexByte(b, 0x1b); last >= 0 {
		state, b = vtEscape, b[last+1:]
	}
	for len(b) > 0 {
		switch state {
		case vtGround:
			return vtEnd(table, b)
		case vtOSC:
			i := bytes.IndexAny(b, "\x07\x18\x1a")
			if i < 0 {
				return state
			}
			state, b = table[state][b[i]], b[i+1:]
		default:
			next, used := vtUntil(table, state, b)
			state, b = next, b[used:]
		}
	}
	return state
}

// writeOutput writes show byte-for-byte without modification, flushing any
// deferred OSC 777 notifications at the first ground-state boundary. The
// notifications and the paste guard share one check of the frame for C1
// controls, the costliest of their scans, rather than make one each.
func (a *attacher) writeOutput(ctx context.Context, show []byte) {
	if len(a.pendingNotify) == 0 {
		free := c1Free(show)
		a.outStream.feedChecked(show, free)
		a.writeChecked(ctx, show, free)
		a.writeHeld(ctx)
		return
	}
	start := 0
	for i, c := range show {
		a.outStream.step(c)
		if len(a.pendingNotify) > 0 && a.outStream.ground() {
			a.write(ctx, show[start:i+1])
			if a.guard.idle() {
				a.flushPendingNotify(ctx)
			}
			start = i + 1
		}
	}
	if start < len(show) {
		a.write(ctx, show[start:])
	}
	a.writeHeld(ctx)
}

func (a *attacher) emitNotify(ctx context.Context, seq []byte) {
	if a.outStream.ground() && a.guard.idle() {
		a.write(ctx, seq)
		return
	}
	if len(a.pendingNotify) >= maxPendingNotify {
		a.pendingNotify = a.pendingNotify[1:]
	}
	a.pendingNotify = append(a.pendingNotify, seq)
}

func (a *attacher) flushPendingNotify(ctx context.Context) {
	for _, seq := range a.pendingNotify {
		a.write(ctx, seq)
	}
	a.pendingNotify = a.pendingNotify[:0]
}

// notifyOthers shows a desktop notification, through the local terminal, when
// another session on this host starts to need the user or ends.
func (a *attacher) notifyOthers(ctx context.Context, list []Session) {
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
			if r := s.Reason; r != "" {
				body += ": " + r
			}
		case "exited", "errored":
			body = s.Name + " ended"
		default:
			continue
		}
		title := brand.DisplayName + " · " + a.o.Host.Name
		a.emitNotify(ctx, []byte("\x1b]777;notify;"+OSCText(title)+";"+strings.TrimSpace(OSCText(body))+"\x1b\\"))
	}
}

func oscSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == ';' {
			return ' '
		}
		return r
	}, s)
	return api.CleanText(s, 500)
}

// notify raises a system notification (OSC 777) about something that the
// user did, such as an upload. It draws nothing on the screen, so the
// session keeps every row and column. No such notice is ever dropped. Its
// text is the client's own, and names every file of a drop, so it is
// cleaned but not cut. Text from the host in it is cut where it enters.
func (a *attacher) notify(ctx context.Context, msg string) {
	title := brand.DisplayName + " · " + a.o.Host.Name
	a.notice(ctx, []byte("\x1b]777;notify;"+OSCText(title)+";"+oscText(msg, len(msg))+"\x1b\\"), true)
}

// flash shows the reconnect notice on the bottom row: the one thing the
// client draws while attached, and only while the link is down.
func (a *attacher) flash(ctx context.Context, msg string) {
	_, rows, _, _ := a.size()
	a.write(ctx, []byte(fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[2K\x1b[2m %s · %s\x1b[0m\x1b8", rows, brand.DisplayName, msg)))
	a.outStream.reset()
}

func (a *attacher) clearFlash(ctx context.Context) {
	_, rows, _, _ := a.size()
	a.write(ctx, []byte(fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[2K\x1b8", rows)))
	a.outStream.reset()
}
