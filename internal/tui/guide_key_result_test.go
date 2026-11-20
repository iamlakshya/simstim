package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

func TestRentKeyResultPreservesFailureAndAcceptsFoundKey(t *testing.T) {
	failure := errors.New("key command exited with status 17")
	for _, tc := range []struct {
		name string
		key  string
		err  error
		fail bool
	}{
		{"command-failed", "", failure, true},
		{"key-made", "ssh-ed25519 fixture", nil, false},
		{"key-found-despite-error", "ssh-ed25519 fixture", failure, false},
		{"ordinary-empty-lookup", "", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &guide{step: gdRentKey, seq: 8, err: errors.New("old failure")}
			msg := rentKeyMsg{scope: g.scope(), key: tc.key, err: tc.err}
			_ = g.onMsg(nil, msg)
			if g.seq != 9 || g.plan.Key != tc.key || g.step != gdRentKey {
				t.Fatalf("result changed its ownership/phase incorrectly: seq=%d key=%q step=%d", g.seq, g.plan.Key, g.step)
			}
			if tc.fail {
				var ue *client.UserError
				if !errors.As(g.err, &ue) || ue.Code != "ssh_key_failed" || ue.Hint != onboard.KeyCommand || !strings.Contains(ue.Msg, failure.Error()) || !errors.Is(g.err, failure) {
					t.Errorf("key failure lost error or recovery command: err=%v ue=%+v", g.err, ue)
				}
			} else if g.err != nil {
				t.Errorf("key/lookup result retained an obsolete failure: %v", g.err)
			}
			// The consumed reply cannot later replace a newer result.
			g.plan.Key = "newer-key"
			_ = g.onMsg(nil, msg)
			if g.plan.Key != "newer-key" || g.seq != 9 {
				t.Fatal("a repeated key result was accepted twice")
			}
		})
	}
}

func TestRentKeyResultRejectsStaleOwnershipAndBusyPhase(t *testing.T) {
	for _, mode := range []string{"owner", "request", "result-phase", "current-phase", "busy"} {
		t.Run(mode, func(t *testing.T) {
			old := errors.New("keep this current failure")
			g := &guide{step: gdRentKey, seq: 11, err: old}
			g.plan.Key = "current-key"
			msg := rentKeyMsg{scope: g.scope(), key: "stale-key", err: errors.New("stale error")}
			switch mode {
			case "owner":
				msg.scope.owner = &guide{}
			case "request":
				msg.scope.seq--
			case "result-phase":
				msg.scope.step = gdRentUser
			case "current-phase":
				g.step = gdRentUser
			case "busy":
				g.run = true
			}
			_ = g.onMsg(nil, msg)
			if g.seq != 11 || g.plan.Key != "current-key" || !errors.Is(g.err, old) {
				t.Errorf("stale/busy reply changed the guide: seq=%d key=%q err=%v", g.seq, g.plan.Key, g.err)
			}
		})
	}
}
