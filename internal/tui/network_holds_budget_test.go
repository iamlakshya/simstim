package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

// This records UI dispatch only. The real client, Source, physical frame and
// native file publication are covered by their own operation tests.
type positiveHeldPolicyRecorderV1 struct {
	*heldPolicyRecorder
	answers []api.NetworkHoldActionArgs
}

func (c *positiveHeldPolicyRecorderV1) AnswerNetworkRequest(ctx context.Context, args api.NetworkHoldActionArgs) (api.NetworkHoldAnswerResult, error) {
	if err := ctx.Err(); err != nil {
		return api.NetworkHoldAnswerResult{}, err
	}
	c.answers = append(c.answers, args)
	result := api.NetworkHoldAnswerResult{Version: api.NetworkHoldV1, Scope: args.Scope}
	if args.Scope == api.NetworkHoldOnce {
		result.MemoryOnly, result.RuleIDs = true, []string{strings.Repeat("c", 32)}
	}
	return result, nil
}

func TestUploadHoldSheetOffersOnceAndRefuseForNonSensitiveBudget(t *testing.T) {
	hs, h, d, ordinary := heldPolicyFixture(t)
	c := &positiveHeldPolicyRecorderV1{heldPolicyRecorder: ordinary}
	h.conn = cleanConn{c}
	c.snapshot.Requests[0].Reason = api.NetworkHoldUploadBudget
	c.snapshot.Requests[0].Host, c.snapshot.Requests[0].Redacted = "upload.example", false
	c.snapshot.Revision++
	hs.key("ctrl+r")
	wanted := c.snapshot.Requests[0].ID
	if view := d.view(hs.m); !strings.Contains(view, "Upload budget reached") || !strings.Contains(view, "network.upload_budget") || !strings.Contains(view, "once or refuse") {
		t.Fatal("budget hold was drawn as an ordinary unknown-site question", view)
	}
	for _, key := range []string{"enter", "r", "f", "a"} {
		hs.key(key)
		if d.err == nil || d.adding || len(c.answers) != 0 || c.allows.Load() != 0 {
			t.Fatal("budget hold offered a wider or ordinary target exception", key)
		}
	}
	hs.key("o")
	if len(c.answers) != 1 || c.answers[0].Request != wanted || c.answers[0].Scope != api.NetworkHoldOnce || c.answers[0].Session != d.id ||
		c.answers[0].Revision != 0 || c.answers[0].Maximum != "" || c.allows.Load() != 0 {
		t.Fatal("once did not retain the opaque ID or supplied display authority", c.answers)
	}
	hs.key("x")
	if len(c.refused) != 1 || c.refused[0] != d.id+"/"+wanted {
		t.Fatal("budget refusal lost its original opaque identity", c.refused)
	}
}

func TestUnknownHoldKeepsSessionActionAndRetiredSheetCannotAnswer(t *testing.T) {
	hs, h, d, ordinary := heldPolicyFixture(t)
	c := &positiveHeldPolicyRecorderV1{heldPolicyRecorder: ordinary}
	h.conn = cleanConn{c}
	hs.key("enter")
	if len(c.answers) != 1 || c.answers[0].Scope != api.NetworkHoldSession || c.allows.Load() != 0 {
		t.Fatal("ordinary held session action was routed through a raw target grant", c.answers)
	}
	queued := hs.m.answerPolicyHold(d, api.NetworkHoldOnce)
	if queued == nil {
		t.Fatal("original held action was not queued")
	}
	hs.key("esc")
	_ = queued()
	if len(c.answers) != 1 {
		t.Fatal("a closed sheet answered its old held request")
	}
}
