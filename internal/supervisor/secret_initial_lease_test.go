package supervisor

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func TestBoundInitialPolicyRecognisesEndBeforeInitialReply(t *testing.T) {
	q := newEgressEvents()
	rule := api.Rule{ID: "lease", GrantID: "exact-lease", Once: true}
	args := egress.BoundInitArgs{Version: egress.MemberRegistryV2, Initial: egress.InitArgs{Policy: api.Policy{Rules: []api.Rule{rule}}}}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	finish := q.preparePolicy(egress.CtlBoundInit, raw)
	if !q.enqueue(onceEvent(rule), 128) {
		t.Fatal("bound-init pending lease was not admitted")
	}
	finish(true)
	ended := q.takeEnds(0, time.Now(), false)
	if len(ended) != 1 || ended[0].identity != ruleLeaseIdentity(rule) {
		t.Fatal("pre-reply bound-init lease end was lost")
	}
}
