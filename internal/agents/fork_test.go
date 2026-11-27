package agents

import (
	"slices"
	"testing"
)

func TestForkCommandUsesCanonicalDataAndValidatedConversation(t *testing.T) {
	claude, _ := Find("claude")
	id := "3f2b8c1e-9d4a-4e6b-8f7c-1a2b3c4d5e6f"
	command, err := claude.ForkCommand(id)
	if err != nil || !slices.Equal(command, []string{"claude", "--resume", id, "--fork-session"}) {
		t.Fatalf("fork = %q, %v", command, err)
	}
	for _, invalid := range []string{"", "--dangerously-skip-permissions", id + "\n"} {
		fallback, err := claude.ForkCommand(invalid)
		if err != nil || !slices.Equal(fallback, claude.Continue) {
			t.Errorf("invalid conversation %q entered fork: %q, %v", invalid, fallback, err)
		}
	}
	command[0] = "changed"
	if claude.Fork[0] != "claude" {
		t.Fatal("returned command aliases agent data")
	}
	codex, _ := Find("codex")
	fallback, err := codex.ForkCommand(id)
	if err != nil || !slices.Equal(fallback, codex.Continue) {
		t.Fatalf("continue fallback = %q, %v", fallback, err)
	}
	fallback[0] = "changed"
	if codex.Continue[0] != "codex" {
		t.Fatal("fallback aliases agent data")
	}
	if _, err := (Agent{}).ForkCommand(id); err == nil {
		t.Fatal("agent without a continuation silently started a fresh conversation")
	}
}
