package egress

import (
	"encoding/base64"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestRequestTripwireOnlyViewKeepsColdPublicStandinsOutOfEveryCarrier(t *testing.T) {
	base, err := newRequestSecretSnapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	const old = "cold-public-standin"
	provided := []api.Secret{old, old}
	view, err := base.withTripwireSentinels(provided)
	if err != nil {
		t.Fatal(err)
	}
	provided[0] = "changed"
	if view == base || len(view.entries) != 0 || len(view.tripwires) != 1 || len(base.tripwires) != 0 {
		t.Fatal("tripwire union mutated eligibility or retained a mutable caller slice")
	}
	for _, tc := range []struct {
		target string
		header http.Header
	}{
		{"/v1?value=" + old, nil},
		{"/v1?value=%2563old-public-standin", nil},
		{"/v1", http.Header{"X-Record": []string{old}}},
		{"/v1", http.Header{"X-Record": []string{"Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+old))}}},
		{"/v1", http.Header{"X-" + strings.ToUpper(old): []string{"ordinary"}}},
		{"/v1", http.Header{"Authorization": []string{"Bearer " + old}}},
	} {
		request, raw, checked := requestSecretFixture(t, "https", "other.example.com", tc.target, tc.header)
		if failure := checkRequestSecretTripwires(view, request, raw, checked, "https"); failure == nil || failure.status != http.StatusForbidden {
			t.Fatal("cold public stand-in disappeared from pre-policy tripwires")
		}
		plan, failure := prepareRequestSecrets(view, request, raw, checked, "https")
		if failure == nil || len(plan.replacements) != 0 || len(plan.usedNames()) != 0 || len(plan.usedIdentities()) != 0 {
			t.Fatal("tripwire-only stand-in acquired injection or resource identity")
		}
	}
	request, raw, checked := requestSecretFixture(t, "https", "other.example.com", "/ordinary", nil)
	plan, failure := prepareRequestSecrets(view, request, raw, checked, "https")
	if failure != nil || len(plan.replacements) != 0 || plan.generation != view {
		t.Fatal("public tripwire-only view inferred a destination binding or blocked unrelated data")
	}
	if failure := view.checkRequestSecretTrailers(http.Header{"X-Checksum": []string{old}}, "other.example.com:443"); failure == nil {
		t.Fatal("cold stand-in disappeared from the late trailer tripwire")
	}
}

func TestRequestTripwireEnrichmentCannotAuthoriseHistoryAsANamedCredential(t *testing.T) {
	current := requestSecretTestSnapshot(t, "X-Api-Key", "{value}", "real-key-000000", "fake-key-000000", true)
	view, err := current.withTripwireSentinels([]api.Secret{"old-public-key"})
	if err != nil {
		t.Fatal(err)
	}
	request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"X-Api-Key": []string{"fake-key-000000"}})
	plan, failure := prepareRequestSecrets(view, request, raw, checked, "https")
	if failure != nil || len(plan.replacements) != 1 || plan.replacements[0].value != "real-key-000000" || !reflect.DeepEqual(plan.usedIdentities(), []string{"fixture-primary"}) {
		t.Fatal("enrichment changed the current request variant")
	}
	request, raw, checked = requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"X-Api-Key": []string{"old-public-key"}})
	if plan, failure := prepareRequestSecrets(view, request, raw, checked, "https"); failure == nil || len(plan.replacements) != 0 {
		t.Fatal("history-only public stand-in became a request credential")
	}
	for _, bad := range []api.Secret{"", "bad\x00standin"} {
		if _, err := view.withTripwireSentinels([]api.Secret{bad}); err == nil {
			t.Fatal("invalid public tripwire stand-in was published")
		}
	}
}

func TestRequestTripwirePrecheckDoesNotPinArmOrConsumeAnActualOnceRule(t *testing.T) {
	snapshot := requestSecretTestSnapshot(t, "X-Api-Key", "{value}", "", "fake-key-000000", false)
	request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/allowed", http.Header{"X-Api-Key": []string{"fake-key-000000"}})
	if failure := checkRequestSecretTripwires(snapshot, request, raw, checked, "https"); failure != nil {
		t.Fatal("valid named unarmed stand-in was pinned or armed before policy")
	}
	policy, _, err := MergeLayers([]api.Layer{{Kind: api.LayerSession, Preset: api.PresetLocked, Rules: []api.Rule{{Allow: true, Door: api.DoorNet, Target: "api.example.com/allowed", Once: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine("shell", policy)
	if err != nil {
		t.Fatal(err)
	}
	if decision := engine.evaluateHTTP(checked, nil, evalPrecheck); !decision.Allow {
		t.Fatal("existing canonical policy precheck refused its valid once rule")
	}
	if _, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure == nil || failure.status != http.StatusUnauthorized || !strings.Contains(failure.detail.Hint, "Re-mint") {
		t.Fatal("post-policy missing credential lost accurate configured/server-issued recovery")
	}
	if !engine.Decide(checked).Allow || engine.Decide(checked).Allow {
		t.Fatal("pure tripwire, policy precheck or unarmed plan consumed the actual once budget")
	}
	request, raw, checked = requestSecretFixture(t, "https", "api.example.com", "/allowed", http.Header{"X-Api-Key": []string{"attacker-key"}})
	if failure := checkRequestSecretTripwires(snapshot, request, raw, checked, "https"); failure != nil {
		t.Fatal("unknown credential identity was pinned inside the tripwire-only phase")
	}
	if _, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure == nil || failure.status != http.StatusForbidden {
		t.Fatal("post-policy credential pin did not precede unarmed401")
	}
}

func TestRequestTripwirePrecheckLeavesUnknownMalformedBasicForPostPolicyPin(t *testing.T) {
	snapshot := requestSecretTestSnapshot(t, "Authorization", "Bearer {value}", "real-key-000000", "fake-key-000000", true)
	for _, header := range []string{"Basic not-base64", "Basic YTp="} {
		request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/denied", http.Header{"Authorization": {header}})
		if failure := checkRequestSecretTripwires(snapshot, request, raw, checked, "https"); failure != nil {
			t.Fatal("unknown non-sentinel credential was pinned before network policy")
		}
		if plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure == nil || len(plan.replacements) != 0 {
			t.Fatal("strict post-policy credential validation was weakened")
		}
	}
	request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/denied", http.Header{"X-Record": {"Basic " + base64.StdEncoding.EncodeToString([]byte("user:fake-key-000000"))}})
	if failure := checkRequestSecretTripwires(snapshot, request, raw, checked, "https"); failure == nil || failure.status != http.StatusForbidden {
		t.Fatal("valid encoded public sentinel bypassed the pre-policy tripwire")
	}
}
