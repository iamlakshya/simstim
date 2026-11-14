package egress

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestRequestSecretArmingIsPerMatchedCanonicalVariant(t *testing.T) {
	for _, allMissing := range []bool{false, true} {
		firstValue := api.Secret("real-key-A0000")
		if allMissing {
			firstValue = ""
		}
		snapshot, err := newRequestSecretSnapshot([]requestBoundSecret{
			{Name: "A_SECRET", Identity: "canonical-a", Binding: api.SecretBind{Hosts: []string{"api.example.com"}, Header: "Authorization", Format: "Bearer {value}"}, Value: firstValue, Sentinel: "fake-key-A0000", Armed: !allMissing},
			{Name: "B_SECRET", Identity: "canonical-b", Binding: api.SecretBind{Hosts: []string{"api.example.com"}, Header: "X-Api-Key", Format: "{value}"}, Sentinel: "fake-key-B0000", Armed: false},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			headers http.Header
			status  int
			used    []string
		}{
			{http.Header{"Authorization": []string{"Bearer fake-key-A0000"}}, 0, []string{"canonical-a"}},
			{http.Header{"X-Api-Key": []string{"fake-key-B0000"}}, http.StatusUnauthorized, nil},
			{http.Header{"Authorization": []string{"Bearer fake-key-A0000"}, "X-Api-Key": []string{"attacker"}}, http.StatusForbidden, nil},
			{http.Header{"Authorization": []string{"Bearer fake-key-A0000"}, "X-Api-Key": []string{"fake-key-B0000"}}, http.StatusUnauthorized, nil},
			{nil, 0, nil},
		} {
			request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1", tc.headers)
			plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https")
			want := tc.status
			if allMissing && want == 0 {
				want = http.StatusUnauthorized
			}
			if want != 0 {
				if failure == nil || failure.status != want || len(plan.replacements) != 0 || len(plan.usedIdentities()) != 0 {
					t.Fatal("mixed arming/misuse emitted a plan or selected the wrong refusal")
				}
				continue
			}
			if failure != nil || !reflect.DeepEqual(plan.usedIdentities(), tc.used) {
				t.Fatal("unrelated missing variant blocked an armed identity or anonymous request")
			}
			if len(tc.used) > 0 {
				if len(plan.replacements) != 1 || plan.replacements[0].value != "Bearer real-key-A0000" || !reflect.DeepEqual(plan.usedNames(), []string{"A_SECRET"}) {
					t.Fatal("armed variant lost its value or resource name")
				}
			} else if len(plan.replacements) != 0 || len(plan.usedNames()) != 0 {
				t.Fatal("anonymous mixed-state request inserted credentials or inferred identity")
			}
		}
	}
}

func TestRequestSecretMintedVariantsReuseTheirOwnIdentityUnderConfiguredName(t *testing.T) {
	binding := api.SecretBind{Hosts: []string{"api.example.com"}, Header: "Authorization", Format: "Bearer {value}"}
	snapshot, err := newRequestSecretSnapshot([]requestBoundSecret{
		{Name: "API_TOKEN", Identity: "primary-current", Binding: binding, Value: "real-primary-NEW", Sentinel: "fake-primary-000", Armed: true},
		{Name: "API_TOKEN", Identity: "minted-current", Binding: binding, Value: "real-minted-0000", Sentinel: "fake-minted-0000", Armed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ input, want, identity string }{
		{"Bearer fake-primary-000", "Bearer real-primary-NEW", "primary-current"},
		{"Bearer fake-minted-0000", "Bearer real-minted-0000", "minted-current"},
	} {
		request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"Authorization": []string{tc.input}})
		plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https")
		if failure != nil || len(plan.replacements) != 1 || plan.replacements[0].value != api.Secret(tc.want) || plan.replacements[0].identity != tc.identity || !reflect.DeepEqual(plan.usedIdentities(), []string{tc.identity}) || !reflect.DeepEqual(plan.usedNames(), []string{"API_TOKEN"}) {
			t.Fatal("same-name minted/primary variant was guessed from its configured name")
		}
		ids, names := plan.usedIdentities(), plan.usedNames()
		ids[0] = "changed"
		names[0] = "changed"
		if !reflect.DeepEqual(plan.usedIdentities(), []string{tc.identity}) || !reflect.DeepEqual(plan.usedNames(), []string{"API_TOKEN"}) {
			t.Fatal("caller mutated the plan's trusted resource/response labels")
		}
	}
	request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"Authorization": []string{"Bearer real-primary-OLD"}})
	if plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https"); failure == nil || len(plan.replacements) != 0 {
		t.Fatal("historical raw real value acquired current request authority")
	}
}

func TestRequestSecretCanonicalIdentityRejectsAnyConflictingCurrentVariant(t *testing.T) {
	binding := api.SecretBind{Hosts: []string{"api.example.com"}, Header: "X-Api-Key", Format: "{value}"}
	first := requestBoundSecret{Name: "FIRST", Identity: "opaque-cohort", Binding: binding, Value: "real-key-000000", Sentinel: "fake-key-000000", Armed: true}
	for _, change := range []func(*requestBoundSecret){
		func(entry *requestBoundSecret) { entry.Value = "evil-key-000000" },
		func(entry *requestBoundSecret) { entry.Sentinel = "fake-key-111111" },
		func(entry *requestBoundSecret) { entry.Binding.Hosts = []string{"other.example.com"} },
		func(entry *requestBoundSecret) { entry.Binding.Header = "Api-Key" },
		func(entry *requestBoundSecret) { entry.Binding.Format = "Bearer {value}" },
		func(entry *requestBoundSecret) { entry.Armed = false; entry.Value = "" },
	} {
		second := first
		second.Name = "SECOND"
		change(&second)
		if _, err := newRequestSecretSnapshot([]requestBoundSecret{first, second}); err == nil {
			t.Fatal("one canonical identity accepted conflicting current source data")
		}
	}
	alias := first
	alias.Name = "SECOND"
	snapshot, err := newRequestSecretSnapshot([]requestBoundSecret{alias, first, first})
	if err != nil || len(snapshot.entries) != 1 {
		t.Fatal("identical canonical identity aliases were not coalesced")
	}
	request, raw, checked := requestSecretFixture(t, "https", "api.example.com", "/v1", http.Header{"X-Api-Key": []string{"fake-key-000000"}})
	plan, failure := prepareRequestSecrets(snapshot, request, raw, checked, "https")
	if failure != nil || !reflect.DeepEqual(plan.usedNames(), []string{"FIRST", "SECOND"}) || !reflect.DeepEqual(plan.usedIdentities(), []string{"opaque-cohort"}) {
		t.Fatal("canonical aliases did not all retain resource ownership")
	}
}
