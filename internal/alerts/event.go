// Package alerts sends session events from a host to the user's own
// destinations: a command, an ntfy topic or a webhook (decision 0019).
//
// Each session's holder owns a Dispatcher. It reads the alerts file when the
// file changes, holds needs_input for a short delay, limits how often events
// go out, and delivers them in the background, so a slow destination never
// slows the session. Nothing leaves the host until the user names a
// destination.
package alerts

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
)

// Event kinds.
const (
	NeedsInput = "needs_input" // the agent waits for the user
	TurnDone   = "turn_done"   // the agent finished a long turn
	Exited     = "exited"      // the program ended with code 0
	Failed     = "failed"      // the program ended with another code or a signal
	OOM        = "oom"         // the memory cap stopped a process
	Memory     = "memory"      // the session is close to its memory cap
	Started    = "started"     // the session started
	Restored   = "restored"    // the session started again after a host restart
	Test       = "test"        // sent by the test command only
)

// Kinds lists the events a config may name.
var Kinds = []string{NeedsInput, TurnDone, Exited, Failed, OOM, Memory, Started, Restored}

// DefaultEvents are sent when a config names none.
var DefaultEvents = []string{NeedsInput, TurnDone, Failed, OOM, Memory}

// Version is the version of the event's JSON. It changes only when a field
// changes meaning or goes away; new fields may appear at any time.
const Version = 1

// Session identifies the session an event is about.
type Session struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Agent string `json:"agent"`
	Dir   string `json:"dir"`
}

// Event is one thing that happened to a session, as destinations get it.
type Event struct {
	V        int     `json:"v"`
	Event    string  `json:"event"`
	Host     string  `json:"host"`
	Session  Session `json:"session"`
	Time     string  `json:"time"` // RFC 3339, UTC
	ExitCode *int    `json:"exit_code,omitempty"`
	Signal   string  `json:"signal,omitempty"`
	// Detail is the agent's question. It is sent only when the config turns
	// it on, because it can hold code.
	Detail  string `json:"detail,omitempty"`
	Summary string `json:"summary"`

	// Turn is how long the agent worked, for turn_done. It is not sent.
	Turn time.Duration `json:"-"`
	// Usage is how much of its memory cap the session uses, such as "2.7 of
	// 2.9 GiB", for memory. It goes into the summary.
	Usage string `json:"-"`

	at   time.Time // when the holder raised it
	gen  uint64    // the session's state generation when it was raised
	test bool      // raised by the test command
}

// ends reports whether the event is the session's end: exited, failed, or an
// oom that stopped the program itself. Only an end carries an exit code.
func (ev Event) ends() bool { return ev.ExitCode != nil }

// JSON returns the event as destinations get it.
func (ev Event) JSON() []byte {
	b, _ := json.Marshal(ev)
	return b
}

// Text is the event as a short message: the summary, then the detail if any.
func (ev Event) Text() string {
	if ev.Detail == "" {
		return ev.Summary
	}
	return ev.Summary + "\n\n" + ev.Detail
}

// summary builds the event's one-line human description, such as
// "auth-fix on box needs you".
func summary(ev Event) string {
	name := ev.Session.Name
	if name == "" {
		name = ev.Session.ID
	}
	on := name + " on " + ev.Host
	var s string
	switch ev.Event {
	case NeedsInput:
		s = on + " needs you"
	case TurnDone:
		s = on + " finished a turn"
		if ev.Turn >= time.Second {
			s += " (" + shortDuration(ev.Turn) + ")"
		}
	case Exited:
		s = on + " ended"
	case Failed:
		s = on + " failed (" + exitText(ev) + ")"
	case OOM:
		s = on + " ran out of memory, and a process was stopped"
	case Memory:
		s = on + " is close to its memory cap"
		if ev.Usage != "" {
			s += " (" + ev.Usage + ")"
		}
	case Started:
		s = name + " started on " + ev.Host
	case Restored:
		s = name + " was restored on " + ev.Host + " after a restart"
	case Test:
		return "Test alert from " + ev.Host
	default:
		s = on + ": " + ev.Event
	}
	if ev.test {
		s = "Test: " + s
	}
	return s
}

func exitText(ev Event) string {
	switch {
	case ev.Signal != "":
		return "signal: " + ev.Signal
	case ev.ExitCode != nil:
		return "exit " + strconv.Itoa(*ev.ExitCode)
	}
	return "no exit code"
}

// shortDuration formats d as "4m12s" or "1h3m".
func shortDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d >= time.Hour {
		d = d.Round(time.Minute)
	}
	s := d.String()
	s = strings.Replace(s, "m0s", "m", 1)
	return strings.Replace(s, "h0m", "h", 1)
}

// env returns the event as environment variables for a command destination.
// Values go only into the environment and stdin, never into a shell string.
func (ev Event) env() []string {
	vars := [][2]string{
		{"EVENT", ev.Event},
		{"EVENT_VERSION", strconv.Itoa(ev.V)},
		{"HOST", ev.Host},
		{"SESSION_ID", ev.Session.ID},
		{"SESSION_NAME", ev.Session.Name},
		{"SESSION_AGENT", ev.Session.Agent},
		{"SESSION_DIR", ev.Session.Dir},
		{"TIME", ev.Time},
		{"SUMMARY", ev.Summary},
		{"SIGNAL", ev.Signal},
		{"DETAIL", ev.Detail},
	}
	if ev.ExitCode != nil {
		vars = append(vars, [2]string{"EXIT_CODE", strconv.Itoa(*ev.ExitCode)})
	}
	out := make([]string, 0, len(vars))
	for _, kv := range vars {
		// The kernel cannot pass a NUL byte in a variable.
		out = append(out, brand.Env(kv[0])+"="+strings.ReplaceAll(kv[1], "\x00", ""))
	}
	return out
}

// knownKind reports whether k is an event a config may name.
func knownKind(k string) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// TestEvent builds the event the test command sends: kind is "test" or any
// event kind, about a made-up session.
func TestEvent(kind, dir string) (Event, error) {
	if kind == "" {
		kind = Test
	}
	if kind != Test && !knownKind(kind) {
		return Event{}, &Error{Msg: fmt.Sprintf("unknown event %q", kind), Hint: "Use one of: test, " + strings.Join(Kinds, ", ") + "."}
	}
	ev := Event{Event: kind, Session: Session{ID: "test", Name: "alerts-test", Agent: "test", Dir: dir}, test: true}
	switch kind {
	case Failed:
		code := 2
		ev.ExitCode = &code
	case Exited:
		code := 0
		ev.ExitCode = &code
	case TurnDone:
		ev.Turn = 4*time.Minute + 12*time.Second
	case NeedsInput:
		ev.Detail = "Do you want to proceed? (a test question)"
	case Memory:
		ev.Usage = "2.7 of 2.9 GiB"
	}
	return ev, nil
}
