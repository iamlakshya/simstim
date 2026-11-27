package cli

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestPolicyRemoveRejectsARealShortIDCollisionBeforeSending(t *testing.T) {
	const sessionID = "abc234de"
	const collisionID = "gcqjkxl"
	host, transport := fakeBox(t, []client.Session{{ID: sessionID, Name: "work", State: api.StateIdle}})
	for _, key := range []string{"XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(key, t.TempDir())
	}
	if os.Getenv("HOME") == "" || os.Getenv("USERPROFILE") == "" {
		t.Fatal("the owned fake fixture has no isolated home")
	}
	transport.HostID = strings.Repeat("a", 32)
	if err := client.SetTierContext(t.Context(), host.Name, client.TierOwn, transport.HostID, ""); err != nil {
		t.Fatal(err)
	}
	transport.Features = []string{"policy"}
	rules := []api.Rule{
		{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "session-collision-7608.example"},
		{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "session-collision-21481.example"},
	}
	for i := range rules {
		rules[i].ID = api.RuleID(rules[i])
		if rules[i].ID != collisionID {
			t.Fatalf("the real session collision changed: %+v", rules[i])
		}
	}
	transport.Answers = map[string]any{"policy.ls": api.Policy{Rules: rules}}
	root := Root()
	root.SetArgs([]string{"policy", "rm", collisionID, "--in", host.Name + "/" + sessionID, "--no-input"})
	err := root.ExecuteContext(client.WithoutInput(t.Context()))
	var user *client.UserError
	if !errors.As(err, &user) || user.Code != "rule_id_ambiguous" || user.Hint == "" {
		t.Fatalf("ambiguous removal was not refused by the canonical selector: %v", err)
	}
	if ops := transport.Ops(); !slices.Contains(ops, "policy.ls") || slices.Contains(ops, "policy.rm") {
		t.Fatalf("ambiguity sent a mutation or bypassed the real policy read: %q", ops)
	}
}

func TestPolicyRemoveCanRepairATargetWithoutReadingCollidingPolicy(t *testing.T) {
	const sessionID = "abc234de"
	host, transport := fakeBox(t, []client.Session{{ID: sessionID, Name: "work", State: api.StateIdle}})
	for _, key := range []string{"XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		t.Setenv(key, t.TempDir())
	}
	transport.HostID = strings.Repeat("a", 32)
	if err := client.SetTierContext(t.Context(), host.Name, client.TierOwn, transport.HostID, ""); err != nil {
		t.Fatal(err)
	}
	transport.Features = []string{"policy"}
	transport.Fail("policy.ls", &proto.Error{Code: "rule_id_ambiguous", Message: "the old policy has colliding IDs", Hint: "Remove a target directly."})
	transport.Answers = map[string]any{"policy.rm": map[string]int{"removed": 1}}
	root := Root()
	root.SetArgs([]string{"policy", "rm", "  session-collision-7608.example  ", "--in", host.Name + "/" + sessionID, "--no-input"})
	if err := root.ExecuteContext(client.WithoutInput(t.Context())); err != nil {
		t.Fatalf("target repair was blocked by a policy merge: %v", err)
	}
	if ops := transport.Ops(); slices.Contains(ops, "policy.ls") || !slices.Contains(ops, "policy.rm") {
		t.Fatalf("target repair read colliding policy or skipped its actual mutation: %q", ops)
	}
}
