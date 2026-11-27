package cli

import (
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestShowPendingPolicyNamesNextLevelWithoutReplacingRunningLevel(t *testing.T) {
	s := client.Session{ID: "root2345", Name: "work", Wall: &api.WallInfo{Level: "process"}, PolicyNextStart: []string{"sandbox.ssh_keys", "sandbox.wall"}, PolicyNextWall: "microvm"}
	text := showPendingPolicy(s)

	details := sessionDetails(s)
	currentFound, pendingFound := false, false
	for _, detail := range details {
		if detail.label == "Wall" && detail.value == "process" {
			currentFound = true
		}
		if detail.label == "Pending policy" && strings.Contains(detail.value, "microvm") {
			pendingFound = true
		}
	}
	if !currentFound || !pendingFound {
		t.Fatal("show omitted applied wall or separate pending level", details)
	}
	if !strings.Contains(text, "sandbox.wall=microvm") || !strings.Contains(text, "at the next start") || s.Wall.Level != "process" {
		t.Fatal("pending policy was confused with applied policy", text)
	}
	if got := showPendingPolicy(client.Session{}); got != "" {
		t.Fatal("absent pending data invented a next-start policy")
	}
}
