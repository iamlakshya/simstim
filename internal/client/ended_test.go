package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

// The out-of-memory mark says that the cap stopped the agent only when the
// agent has ended, and a SIGKILL ended it. Otherwise the cap stopped
// another process: while the agent runs, or before it exited (an older
// holder sets the mark again after the exit, and a newer one gives the
// reason api.ReasonChildOOM instead).
func TestCapStopped(t *testing.T) {
	cases := []struct {
		s                  Session
		ended, stop, child bool
	}{
		{Session{State: api.StateErrored, OOM: true, Signal: "killed"}, true, true, false},
		{Session{State: api.StateErrored, OOM: true, Signal: "SIGKILL"}, true, true, false},
		{Session{State: api.StateErrored, OOM: true, Signal: "kill"}, true, true, false},
		{Session{State: api.StateExited, OOM: true}, true, false, true},
		{Session{State: api.StateErrored, OOM: true, Signal: "terminated"}, true, false, true},
		{Session{State: api.StateErrored, Reason: api.ReasonChildOOM}, true, false, true},
		{Session{State: api.StateErrored, Signal: "killed"}, true, false, false},
		{Session{State: api.StateLost, OOM: true, Signal: "killed"}, true, true, false},
		{Session{State: api.StateWorking, OOM: true, Signal: "killed", Alive: true}, false, false, true},
		{Session{State: api.StateNeedsInput, OOM: true, Alive: true}, false, false, true},
		{Session{State: api.StateIdle, Alive: false}, false, false, false},
	}
	for _, c := range cases {
		if got := Ended(c.s); got != c.ended {
			t.Errorf("Ended(%+v) = %v", c.s, got)
		}
		if got := CapStopped(c.s); got != c.stop {
			t.Errorf("CapStopped(%+v) = %v", c.s, got)
		}
		if got := ChildStopped(c.s); got != c.child {
			t.Errorf("ChildStopped(%+v) = %v", c.s, got)
		}
	}
}
