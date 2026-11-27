package tui

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func TestPolicySheetCannotRemoveAnAmbiguousShortID(t *testing.T) {
	hs, _, sheet, connection := policyLifecycleFixture(t)
	const collisionID = "gcqjkxl"
	sheet.policy.Rules = []api.Rule{
		{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "session-collision-7608.example"},
		{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "session-collision-21481.example"},
	}
	for i := range sheet.policy.Rules {
		rule := &sheet.policy.Rules[i]
		rule.ID = api.RuleID(*rule)
		if rule.ID != collisionID {
			t.Fatalf("the real collision fixture changed: %+v", rule)
		}
	}
	for _, selection := range []int{0, 1} {
		sheet.sel = selection
		if command := hs.m.removePolicy(sheet); command != nil {
			t.Fatal("an ambiguous selected row queued a remote command")
		}
		var user *client.UserError
		if !errors.As(sheet.err, &user) || user.Code != "rule_id_ambiguous" || user.Hint == "" || connection.mutations.Load() != 0 {
			t.Fatalf("selected collision %d was not refused: mutations=%d err=%v", selection, connection.mutations.Load(), sheet.err)
		}
	}
}
