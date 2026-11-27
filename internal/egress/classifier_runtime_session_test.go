package egress

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestClassifierSessionAllowWaitsForActualDurablePublication(t *testing.T) {
	server, _ := classifierFixture(t, "1s", 20)
	policy := api.ClonePolicy(server.currentMembers().metadata.Policy)
	scope := "session"
	policy.Classifier.MaxScope = &scope
	if err := server.replaceControlPolicy(memberJSON(t, PolicyUpdateArgs{Policy: policy})); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	server.ctlMu.Lock()
	server.ctl, server.ctlStream = proto.NewWriter(&output), nil
	server.ctlMu.Unlock()
	server.classifier.testJudgeFactory = func(context.Context, api.Policy) (classify.Judge, error) {
		return runtimeJudge(func(context.Context, classify.Input) (classify.Result, error) {
			return classify.Result{Action: "allow", Reason: "documentation for the current task", Scope: "session"}, nil
		}), nil
	}
	request := Request{Host: "durable.example", Port: 443}
	outcome := server.runUnknownClassifier(t.Context(), request, "explicit", request.Host)
	server.classifier.close()
	if outcome.Applied || outcome.Decision.Allow || !outcome.Pending || outcome.Decision.Rule != "classify:session-pending" || server.currentEngine().Decide(request).Allow {
		t.Fatal("model session answer installed a memory-only grant or reported a model outage")
	}
	server.ctlMu.Lock()
	captured := output.String()
	server.ctlMu.Unlock()
	if !strings.Contains(captured, "classifier_session") || strings.Contains(captured, "judge_unavailable") {
		t.Fatal("durable publication did not use its real opaque-hold event")
	}
}
