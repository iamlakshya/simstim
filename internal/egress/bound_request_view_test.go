package egress

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestBoundRequestProjectionKeepsPartialArmingAndActualVariantIdentities(t *testing.T) {
	args := boundRegistryArgs(map[string]string{"A": "ABCD0000", "B": "WXYZ1111"})
	args.Binds["B"] = api.SecretBind{Hosts: []string{"example.com"}, Header: "X-Api-Key", Format: "{value}"}
	_, saved := newBoundRegistryServer(t, args)
	args.Initial.Secrets = map[string]string{"A": "ABCD0000"}
	args.SourceKinds = map[string][]string{"A": {"shell"}}
	args.Saved = saved.Secrets
	server, initial := newBoundRegistryServer(t, args)
	state := server.currentMembers()
	for _, row := range []struct {
		header, supplied, used string
		status                 int
	}{
		{"Authorization", "Bearer " + string(initial.Secrets["A"].Sentinel), "A", 0},
		{"X-Api-Key", string(initial.Secrets["B"].Sentinel), "", http.StatusUnauthorized},
		{"X-Api-Key", "unapproved", "", http.StatusForbidden},
	} {
		request, raw, checked := requestSecretFixture(t, "https", "example.com", "/v1", http.Header{row.header: []string{row.supplied}})
		plan, failure := prepareRequestSecrets(state.requestView, request, raw, checked, "https")
		if row.status != 0 {
			if failure == nil || failure.status != row.status || len(plan.replacements) != 0 {
				t.Fatal("backend partial arming lost misuse or exact missing-variant precedence")
			}
			continue
		}
		if failure != nil || len(plan.replacements) != 1 || plan.replacements[0].value != "Bearer ABCD0000" || !reflect.DeepEqual(plan.usedNames(), []string{row.used}) || !reflect.DeepEqual(plan.usedIdentities(), []string{initial.Secrets[row.used].Identity}) {
			t.Fatal("unrelated missing credential blocked the actual armed backend identity")
		}
	}
}

func TestBoundRequestProjectionExcludesHistoryAndUsesMintedCurrentVariant(t *testing.T) {
	server, initial := newBoundRegistryServer(t, boundRegistryArgs(map[string]string{"KEY": "ABCD0000"}))
	if _, err := server.armControl(memberJSON(t, ArmArgs{Version: 2, Generation: initial.Generation, Values: map[string]api.Secret{"KEY": "WXYZ1111"}})); err != nil {
		t.Fatal(err)
	}
	minted := api.Secret("sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20))
	mintedSentinel, err := server.registerResponseSecret(t.Context(), "example.com:443", boundPrimaryUse(server, "KEY"), minted, "openai-api-key")
	if err != nil {
		t.Fatal(err)
	}
	state := server.currentMembers()
	mint := state.secrets["KEY"].minted[0]
	if mint.identity == "" || mint.identity == state.secrets["KEY"].identity {
		t.Fatal("minted request variant lacks an independent opaque identity")
	}
	for _, entry := range state.requestEntries {
		if entry.Name != "KEY" || entry.Value == "ABCD0000" {
			t.Fatal("request projection fabricated a name or granted a historical real value")
		}
	}
	for _, row := range []struct {
		input    api.Secret
		want     api.Secret
		identity string
	}{
		{state.secrets["KEY"].bound.sentinel, "WXYZ1111", state.secrets["KEY"].identity},
		{mintedSentinel, minted, mint.identity},
	} {
		request, raw, checked := requestSecretFixture(t, "https", "example.com", "/v1", http.Header{"Authorization": []string{"Bearer " + string(row.input)}})
		plan, failure := prepareRequestSecrets(state.requestView, request, raw, checked, "https")
		if failure != nil || len(plan.replacements) != 1 || plan.replacements[0].value != "Bearer "+row.want || !reflect.DeepEqual(plan.usedIdentities(), []string{row.identity}) || !reflect.DeepEqual(plan.usedNames(), []string{"KEY"}) {
			t.Fatal("backend guessed a primary or minted request variant by configured name")
		}
	}
	request, raw, checked := requestSecretFixture(t, "https", "example.com", "/v1", http.Header{"Authorization": []string{"Bearer ABCD0000"}})
	if plan, failure := prepareRequestSecrets(state.requestView, request, raw, checked, "https"); failure == nil || failure.status != http.StatusForbidden || len(plan.replacements) != 0 {
		t.Fatal("retained response history authorised a raw historical request credential")
	}
}
