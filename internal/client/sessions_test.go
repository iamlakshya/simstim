package client_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/client/clienttest"
)

// A host's list keeps its rows as the host sent them, without a row whose
// ID is not a session ID, which no request could name safely.
func TestSessionsDropsRowsWithoutASessionID(t *testing.T) {
	m := clienttest.NewMachine(client.MachineRunning, &clienttest.Transport{Sessions: []client.Session{
		{ID: "andvnnie", Name: "app\u202e"},
		{ID: "bad\x1b]52;c;aGk=\x07"},
		{ID: "S1"},
	}})
	c, err := client.Dial(context.Background(), register(t, m), false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	list, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "andvnnie" || list[0].Name != "app\u202e" {
		t.Fatalf("Sessions = %+v, want the one valid row, as the host sent it", list)
	}
}

// new and resume answer with a session that the client goes on to attach
// to. An ID that is not a session ID is refused: it would reach requests,
// commands and the terminal's title.
func TestNewAndResumeRefuseABadSessionID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const bad = "x\x07\x1b]52;c;aGk=\x07"
	tr := &clienttest.Transport{Features: []string{"resume"}, Answers: map[string]any{
		"new":    client.Session{ID: bad, Name: "app"},
		"resume": api.ResumeResult{Session: client.Session{ID: bad}, Why: "started again"},
	}}
	c, err := client.Dial(context.Background(), register(t, clienttest.NewMachine(client.MachineRunning, tr)), false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	check := func(what string, err error) {
		t.Helper()
		var ue *client.UserError
		if !errors.As(err, &ue) || !strings.Contains(ue.Msg, "a session ID that is not one") || ue.Hint == "" {
			t.Fatalf("%s = %v, want a refusal of the ID", what, err)
		}
		if strings.ContainsAny(ue.Msg, "\x07\x1b") {
			t.Errorf("%s's refusal holds the host's controls: %q", what, ue.Msg)
		}
	}
	_, err = c.NewSession(context.Background(), client.NewArgs{Agent: "shell"})
	check("new", err)
	_, err = c.Resume(context.Background(), client.ResumeArgs{ID: "abc"})
	check("resume", err)

	tr.Answers["new"] = client.Session{ID: "andvnnie", Name: "app"}
	if s, err := c.NewSession(context.Background(), client.NewArgs{Agent: "shell"}); err != nil || s.ID != "andvnnie" {
		t.Fatalf("new with a good ID = %v, %v", s, err)
	}
}
