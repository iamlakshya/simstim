package cli

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/onboard"
)

func TestGuideRejectsRawSessionIDsBeforeWaiting(t *testing.T) {
	for _, id := range []string{"valid1\u200b", "\u202evalid1", "id; command", "id\nnext", strings.Repeat("a", 65)} {
		t.Run(id, func(t *testing.T) {
			_, tr := withFake(t, nil)
			tr.NewID = id
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var out bytes.Buffer
			g := &guide{ctx: ctx, out: &out}
			h := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
			got, _, err := g.runSession(h, onboard.Action{Args: client.NewArgs{Name: "install"}})
			var ue *client.UserError
			if got != "" || !errors.As(err, &ue) || ue.Code != "bad_session_id" {
				t.Fatalf("ID %q: got %q, %v", id, got, err)
			}
			if ops := tr.Ops(); !slices.Equal(ops, []string{"new"}) {
				t.Fatalf("invalid ID reached another operation: %v", ops)
			}
			if out.Len() != 0 {
				t.Fatalf("invalid ID reached output: %q", out.String())
			}
		})
	}
}

func TestGuideDisplaysCleanSessionNames(t *testing.T) {
	_, tr := withFake(t, []client.Session{{ID: "valid1", State: api.StateExited}})
	tr.NewID = "valid1"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var out bytes.Buffer
	g := &guide{ctx: ctx, out: &out}
	h := client.Host{Name: "box", Machine: &client.Machine{Provider: "fake", ID: "fake-box"}}
	name := "installer\x1b]52;c;value\a\u202e"
	id, ended, err := g.runSession(h, onboard.Action{Args: client.NewArgs{Name: name}})
	if err != nil || !ended || id != "valid1" {
		t.Fatalf("runSession = %q, %v, %v", id, ended, err)
	}
	if strings.ContainsAny(out.String(), "\x1b\a\u202e") || !strings.Contains(out.String(), "box/valid1") {
		t.Fatalf("unsafe or missing output: %q", out.String())
	}
}

func TestGuideStopsWhenCompletionHasNoValidSession(t *testing.T) {
	for _, id := range []string{"", "good1\u200b"} {
		var out bytes.Buffer
		f := onboard.NewFlow("box", nil, nil)
		_ = f.Choose("claude")
		a := f.Next()
		g := &guide{ctx: context.Background(), out: &out,
			session: func(client.Host, onboard.Action) (string, bool, error) { return id, false, nil },
			checks:  func(client.Host) ([]api.AgentCheck, error) { return nil, nil }}
		err := g.inSession(client.Host{Name: "box"}, f, a)
		var ue *client.UserError
		if !errors.As(err, &ue) || ue.Code != "bad_session_id" || out.Len() != 0 || f.Next().Agent.Kind != a.Agent.Kind {
			t.Fatalf("completion continued: %q, %v", out.String(), err)
		}
	}
}
