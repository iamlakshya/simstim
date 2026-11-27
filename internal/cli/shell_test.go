package cli

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestShellLeafIsRegisteredAndHasOnlyTerminalFlags(t *testing.T) {
	root := Root()
	command, _, err := root.Find([]string{"shell"})
	if err != nil || command.Name() != "shell" {
		t.Fatal("shell has no real CLI entrypoint", err)
	}
	for _, name := range []string{"new", "name", "detach", "json"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("shell lacks --%s", name)
		}
	}
	for _, name := range []string{"wall", "mount", "memory-max", "profile", "secret", "set"} {
		if command.Flags().Lookup(name) != nil {
			t.Fatalf("shell silently changes sandbox settings with --%s", name)
		}
	}
}

func TestShellJSONRequiresDetachBeforeHostResolution(t *testing.T) {
	newTestHome(t)
	cmd := shellCmd()
	cmd.SetContext(t.Context())
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"unsaved/a2a2a2a2", "--json"})
	err := cmd.Execute()
	var user *client.UserError
	if !errors.As(err, &user) || user.Code != "json_needs_detach" {
		t.Fatalf("shell reached host resolution or attached in JSON mode: %v", err)
	}
}

func TestShellSelectsNewestRunningMemberOnly(t *testing.T) {
	before := time.Unix(1, 0)
	rows := []client.Session{
		{ID: "b3b3b3b3", SandboxOf: "a2a2a2a2", Agent: "shell", Alive: true, CreatedAt: before},
		{ID: "c4c4c4c4", SandboxOf: "a2a2a2a2", Agent: "shell", Alive: true, CreatedAt: before.Add(time.Second)},
		{ID: "d5d5d5d5", SandboxOf: "a2a2a2a2", Agent: "shell", Alive: true, State: api.StateExited, CreatedAt: before.Add(2 * time.Second)},
		{ID: "e6e6e6e6", SandboxOf: "z9z9z9z9", Agent: "shell", Alive: true, CreatedAt: before.Add(3 * time.Second)},
	}
	snapshot := &api.SandboxSnapshot{Version: 1, Primary: "a2a2a2a2", Members: []string{"b3b3b3b3", "c4c4c4c4", "d5d5d5d5"}}
	selected := runningShellRowsV1(rows, snapshot)
	if len(selected) != 2 || selected[0].ID != "c4c4c4c4" {
		t.Fatal("shell selected an ended or foreign session, or ignored creation order")
	}
	name := nextHostShellNameV1(strings.Repeat("界", api.SessionNameMax), nil)
	if _, _, ok := api.CheckName(name); !ok || !strings.HasSuffix(name, "-sh") {
		t.Fatal("shell made an invalid derived name")
	}
}
