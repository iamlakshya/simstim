package tui

import (
	"context"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

// This is an ordinary transport recorder for the existing launcher harness.
// The client publication tests exercise the real grant gate separately.
type policyRecorder struct {
	Conn
	rules   []api.Rule
	allowed []api.PolicyAddArgs
	removed []api.PolicyRmArgs
}

func (c *policyRecorder) Policy(context.Context, string) (api.Policy, error) {
	return api.Policy{Preset: api.PresetBalanced, Unknown: api.UnknownDeny, Rules: slices.Clone(c.rules)}, nil
}

func (c *policyRecorder) AllowPolicy(_ context.Context, a api.PolicyAddArgs) (api.Rule, error) {
	c.allowed = append(c.allowed, a)
	r := api.Rule{ID: "gabcdef", Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: a.Target, Once: a.Once}
	c.rules = append(c.rules, r)
	return r, nil
}

func (c *policyRecorder) RemovePolicy(_ context.Context, a api.PolicyRmArgs) (int, error) {
	c.removed = append(c.removed, a)
	c.rules = slices.DeleteFunc(c.rules, func(r api.Rule) bool { return r.ID == a.ID })
	return 1, nil
}

func (*policyRecorder) PolicyLog(context.Context, api.PolicyLogArgs) ([]client.PolicyLogEntry, error) {
	return []client.PolicyLogEntry{}, nil
}

func TestPolicySheetUsesRealOperationsThroughTheCleaningWrapper(t *testing.T) {
	hs := sessions(t, 100, 36)
	h, selected := hs.m.selectedPair()
	if selected == nil {
		t.Fatal("fixture has no selected session")
	}
	selected.Wall = &api.WallInfo{Level: "process", Driver: "process"}
	recorder := &policyRecorder{Conn: unclean(h.conn)}
	h.conn = cleanConn{recorder}
	hs.key("p")
	d, ok := hs.m.modal.(*policySheet)
	if !ok || d.loading || d.err != nil {
		t.Fatalf("policy did not read through the real wrapper: %+v", hs.m.modal)
	}
	hs.key("a")
	hs.typeText("api.example.com")
	hs.key("enter")
	if len(recorder.allowed) != 1 || recorder.allowed[0].ID != selected.ID || recorder.allowed[0].Target != "api.example.com" || recorder.allowed[0].Once {
		t.Fatalf("wrong allowance: %+v", recorder.allowed)
	}
	if len(d.policy.Rules) != 1 || d.policy.Rules[0].Layer != api.LayerSession {
		t.Fatal("the sheet did not reload the actual result")
	}
	hs.key("backspace")
	if len(recorder.removed) != 1 || recorder.removed[0].ID != "gabcdef" || len(d.policy.Rules) != 0 {
		t.Fatalf("wrong revocation or stale sheet: %+v, %+v", recorder.removed, d.policy.Rules)
	}
}

func TestPolicySheetDoesNotEditAnotherLayerOrInstallAStaleAnswer(t *testing.T) {
	d := &policySheet{policy: api.Policy{Rules: []api.Rule{{ID: "gabcdef", Layer: api.LayerHost, Target: "api.example.com"}}}}
	m := &Model{modal: d}
	if cmd := m.removePolicy(d); cmd != nil || d.err == nil {
		t.Fatal("the session sheet tried to remove a wider rule")
	}
	m.modal = nil
	if cmd := m.onPolicy(policyMsg{owner: d, notice: "stale grant"}); cmd != nil || m.modal != nil {
		t.Fatal("a closed policy sheet installed a stale answer")
	}
}
