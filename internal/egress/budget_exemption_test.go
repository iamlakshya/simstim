package egress

import (
	"net/http"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

func TestActualModelAndValidatedPushUseTheirOriginalBudgetExclusions(t *testing.T) {
	policy := api.Policy{Preset: api.PresetLocked, LayerValues: api.LayerValues{Network: api.NetworkValues{UploadBudget: new("0M")}}}
	model := newModelHTTPFixture(t, "codex", policy, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"total_tokens":1}}`))
	}))
	response, _ := model.request(t, "POST", "api.openai.com", "/v1/responses", []byte(`{"model":"model-a","input":"`+strings.Repeat("data", 1024)+`"}`), "")
	if response.Code != http.StatusOK || model.requests.Load() != 1 || len(model.env.srv.pendingNetworkSnapshot().Requests) != 0 {
		t.Fatal("fixed model API classification lost its original exclusion")
	}
	prefix := "refs/heads/" + brand.Name + "/model234567/"
	body := append(receiveTestPacket(receiveTestCommand(prefix+"fix", 40, false)+"\x00report-status"), []byte("0000PACK"+strings.Repeat("p", 4096))...)
	policy.Rules = []api.Rule{pushTestRule()}
	push := newPushHTTPFixture(t, policy, body)
	accepted, _ := pushFixtureRequest(t, push, "POST", "/org/repo.git/git-receive-pack", body)
	if accepted.Code != http.StatusOK || push.requests.Load() != 1 || len(push.env.srv.pendingNetworkSnapshot().Requests) != 0 {
		t.Fatal("original inspected push command/grant did not supply its exclusion")
	}
	refused, _ := pushFixtureRequest(t, push, "POST", "/org/repo.git/git-receive-pack", body)
	if refused.Code != http.StatusForbidden || push.requests.Load() != 1 {
		t.Fatal("upload exclusion revived the original spent push-once authority")
	}
}
