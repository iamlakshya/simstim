package egress

import (
	"reflect"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestModelAtomicAdmissionReusesRequiredSlotAndPermanentToolNetwork(t *testing.T) {
	request := Request{Host: "api.openai.com", Port: 443, Method: "POST", Path: "/v1/responses"}
	for _, test := range []struct {
		name      string
		rules     []api.Rule
		features  []string
		wantOwned int
		wantTools string
	}{
		{"files must not pay for mcp", []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "api.openai.com", Tools: []string{"files"}, Once: true, GrantID: "files"}, {Layer: api.LayerSession, Allow: true, Target: "api.openai.com", Tools: []string{"mcp"}, Once: true, GrantID: "mcp"}}, []string{"mcp"}, 1, "mcp"},
		{"permanent tool supplies networking", []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "api.openai.com", Tools: []string{"mcp"}}, {Layer: api.LayerSession, Allow: true, Target: "api.openai.com", Once: true, GrantID: "plain"}}, []string{"mcp"}, 0, ""},
		{"one combined lease covers both", []api.Rule{{Layer: api.LayerSession, Allow: true, Target: "api.openai.com", Tools: []string{"mcp"}, Once: true, GrantID: "mcp-only"}, {Layer: api.LayerSession, Allow: true, Target: "api.openai.com", Tools: []string{"mcp", "url_content"}, Once: true, GrantID: "combined"}}, []string{"mcp", "url_content"}, 1, "combined"},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Rules: test.rules})
			if err != nil {
				t.Fatal(err)
			}
			permit := engine.NewPermit()
			defer permit.Close()
			model := &modelRequest{request: request, features: test.features}
			if refusal := engine.admitModelRequest(model, permit, true, evalReserve); refusal != "" {
				t.Fatal(refusal)
			}
			if len(permit.owned) != test.wantOwned {
				t.Fatalf("reserved %d slots, want %d", len(permit.owned), test.wantOwned)
			}
			if test.wantOwned != 0 {
				for slot := range permit.owned {
					if slot.key.grantID != test.wantTools {
						t.Fatalf("spent unrelated lease %s", slot.key.grantID)
					}
				}
			}
		})
	}
}

func TestModelFailedAtomicAdmissionHasNoReservationsAndSharesDenies(t *testing.T) {
	grant := api.Rule{Layer: api.LayerSession, Allow: true, Target: "api.openai.com", Tools: []string{"mcp"}, Once: true, GrantID: "only-mcp"}
	engine, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{grant}})
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotDNSLedger(engine)
	request := Request{Host: "API.OPENAI.COM.", Port: 443, Method: "POST", Path: "/v1/responses"}
	permit := engine.NewPermit()
	defer permit.Close()
	if got := engine.admitModelRequest(&modelRequest{request: request, features: []string{"mcp", "url_content"}}, permit, true, evalReserve); got != "url_content" {
		t.Fatal(got)
	}
	if len(permit.owned) != 0 || !reflect.DeepEqual(before, snapshotDNSLedger(engine)) {
		t.Fatal("failed full request exposed a partial reservation")
	}
	if engine.modelGuard(request) == nil {
		t.Fatal("canonical host alias lost its guard")
	}
	for _, lock := range []bool{false, true} {
		policy := api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{grant}}
		deny := api.Rule{Layer: api.LayerSession, Target: "api.openai.com/v1/responses"}
		if lock {
			policy.Locks = []api.Lock{{Layer: api.LayerHost, Deny: []string{deny.Target}}}
		} else {
			policy.Rules = append(policy.Rules, deny)
		}
		blocked, err := NewEngine("shell", policy)
		if err != nil {
			t.Fatal(err)
		}
		p := blocked.NewPermit()
		if blocked.modelToolAllowed(request, "mcp", p, evalReserve) || len(p.owned) != 0 {
			t.Fatal("tool admission spent a slot ahead of a deny or lock")
		}
		p.Close()
	}
	bad := request
	bad.Path = "/v1/../responses"
	if engine.modelToolAllowed(bad, "mcp", permit, evalReserve) || len(permit.owned) != 0 {
		t.Fatal("noncanonical path acquired capability authority")
	}
}

func TestModelTransactionSamplesCurrentTimeUnderItsSingleLedger(t *testing.T) {
	engine := modelTestEngine(t, "codex", api.PresetLocked, "mcp")
	engine.now = func() time.Time {
		if engine.ledger.mu.TryLock() {
			engine.ledger.mu.Unlock()
			t.Error("model transaction sampled admission outside ledger ownership")
		}
		return time.Now()
	}
	request := Request{Host: "api.openai.com", Port: 443, Method: "POST", Path: "/v1/responses"}
	permit := engine.NewPermit()
	defer permit.Close()
	if refusal := engine.admitModelRequest(&modelRequest{request: request, features: []string{"mcp"}}, permit, true, evalReserve); refusal != "" {
		t.Fatal(refusal)
	}
}
