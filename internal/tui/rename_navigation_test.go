package tui

import (
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestRenameNavigationKeepsUneditedHostileName(t *testing.T) {
	for _, key := range []string{"left", "right", "home", "end", "ctrl+a", "ctrl+b", "ctrl+e", "ctrl+f"} {
		t.Run(key, func(t *testing.T) {
			const raw = "auth\u202e"
			be := labBackend()
			be.state["atlas-dev"].sessions = []client.Session{{
				ID: "s1", Name: raw, Agent: "shell", State: api.StateWorking,
				Alive: true, Since: ago(time.Minute), CreatedAt: ago(time.Hour),
			}}
			hs := newHarness(t, be, 80, 24)
			hs.key("enter", "r", key, "enter")
			if hs.m.rename != nil {
				t.Fatal("unchanged rename did not close")
			}
			if got := be.state["atlas-dev"].sessions[0].Name; got != raw {
				t.Fatalf("navigation renamed the session to %q, want %q", got, raw)
			}
		})
	}
}

func TestPickerSearchUsesDisplayedAddress(t *testing.T) {
	p := newPicker("attach", []PickRow{{Host: "box", Session: client.Session{ID: "abcd2345", Name: "au\u200bth-fix"}}}, func() time.Time { return pickNow })
	p.filter = []rune("auth")
	rows := p.matches()
	if len(rows) != 1 || rows[0].Session.ID != "abcd2345" {
		t.Fatalf("search of displayed auth-fix returned %#v", rows)
	}
}

func TestRenameNoopDoesNotChangeHostileName(t *testing.T) {
	for _, keys := range [][]string{{"home", "backspace"}, {"end", "delete"}, {"end", "ctrl+k"}, nil} {
		const raw = "auth\u202e"
		be := labBackend()
		be.state["atlas-dev"].sessions = []client.Session{{ID: "s1", Name: raw, Agent: "shell", State: api.StateWorking, Alive: true}}
		hs := newHarness(t, be, 80, 24)
		hs.key("enter", "r")
		hs.key(keys...)
		if keys == nil {
			hs.m.onPaste("\u202e\u200b\x00")
		}
		hs.key("enter")
		if got := be.state["atlas-dev"].sessions[0].Name; got != raw {
			t.Errorf("no-op keys %q renamed to %q", keys, got)
		}
	}
}
