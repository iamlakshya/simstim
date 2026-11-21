package client

import (
	"reflect"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func armNeedFixtureV1() (*trustedJudgePlanV1, *api.SandboxCohortView, *api.TrustedSecretReadinessQueryV1, map[string]api.SecretBind) {
	bind := api.SecretBind{Hosts: []string{"model.example:443"}, Header: "Authorization", Format: "Bearer {value}"}
	judge := &trustedJudgePlanV1{name: "KEY", use: api.TrustedSecretUseV1{Version: 1, Use: api.TrustedJudgeUseV1, Mode: api.SecretModeBound, Bind: bind, Model: "model", Endpoint: "https://model.example/v1/chat/completions"}}
	query := &api.TrustedSecretReadinessQueryV1{Version: 1, Generation: "original", Uses: map[string]api.TrustedSecretUseV1{"KEY": judge.ownedUse()}}
	view := &api.SandboxCohortView{SecretState: &api.SessionSecretState{Version: api.SessionSecretStateV1, Generation: "original", Intent: api.SecretDeliveryIntent{Version: 1, Modes: map[string]api.SecretMode{"KEY": api.SecretModeBound, "HOST_ONLY": api.SecretModeBound}, Binds: map[string]api.SecretBind{"KEY": bind, "HOST_ONLY": bind}, TrustedUses: query.Uses}, Primary: map[string]api.PublicSecretState{"KEY": {Mode: api.SecretModeBound, Armed: true}, "HOST_ONLY": {Mode: api.SecretModeBound}}}, TrustedReadiness: &api.TrustedSecretReadinessV1{Version: 1, Generation: "original", Unarmed: []string{}}}
	return judge, view, query, map[string]api.SecretBind{"KEY": bind}
}

func TestArmReadyRolesSkipWithoutSelectingHostOnlyNames(t *testing.T) {
	judge, view, query, binds := armNeedFixtureV1()
	needed, judgeNeed, err := armSecretsNeededV1([]string{"KEY"}, binds, judge, view, query)
	if err != nil || len(needed) != 0 || judgeNeed {
		t.Fatal("ready roles refetched or host-only names expanded the original local selection", err)
	}
	view.SecretState.Primary["KEY"] = api.PublicSecretState{Mode: api.SecretModeBound}
	needed, judgeNeed, err = armSecretsNeededV1([]string{"KEY"}, binds, nil, view, nil)
	if err != nil || !reflect.DeepEqual(needed, []string{"KEY"}) || judgeNeed {
		t.Fatal("ordinary bound-only arm required a classifier or selected a host-only name", err)
	}
}

func TestArmDualRoleNeedRequiresBothSeparatelyAuthorisedWrites(t *testing.T) {
	for _, guestNeed := range []bool{false, true} {
		judge, view, query, binds := armNeedFixtureV1()
		if guestNeed {
			view.SecretState.Primary["KEY"] = api.PublicSecretState{Mode: api.SecretModeBound}
		} else {
			view.TrustedReadiness.Unarmed = []string{"KEY"}
		}
		needed, judgeNeed, err := armSecretsNeededV1([]string{"KEY"}, binds, judge, view, query)
		if err != nil || !judgeNeed || !reflect.DeepEqual(needed, []string{"KEY"}) {
			t.Fatal("one needed role skipped the other role's separate permission and same-value write", err)
		}
	}
}

func TestArmMissingChangedOrIncompleteReadinessCannotSkipOrRefetch(t *testing.T) {
	for _, change := range []string{"missing", "null", "generation", "extra"} {
		judge, view, query, binds := armNeedFixtureV1()
		switch change {
		case "missing":
			view.TrustedReadiness = nil
		case "null":
			view.TrustedReadiness.Unarmed = nil
		case "generation":
			view.TrustedReadiness.Generation = "another"
		case "extra":
			view.TrustedReadiness.Unarmed = []string{"HOST_ONLY"}
		}
		if _, _, err := armSecretsNeededV1([]string{"KEY"}, binds, judge, view, query); err == nil {
			t.Fatal("incomplete or changed readiness became a skip or a value-fetch selection")
		}
	}
}

func TestArmDTOCannotReplaceOriginalCapsule(t *testing.T) {
	args := api.ArmSecretsArgs{ID: "arm23456", Generation: "original", Secrets: map[string]api.Secret{"KEY": "fabricated"}}
	if err := beforeArmSecretsRequestV1(t.Context(), nil, proto.Request{Op: "arm"}, args); err == nil {
		t.Fatal("ordinary guest arm DTO manufactured original local selection authority")
	}
	if err := beforeTrustedJudgeRequestV1(t.Context(), nil, proto.Request{Op: "arm"}, map[string]any{"secrets": map[string]string{"KEY": "fabricated"}}); err == nil {
		t.Fatal("generic arm DTO bypassed the actual physical writer guard")
	}
}

func TestArmFreshNegativeReadinessCanSelectChangedCurrentRole(t *testing.T) {
	judge, view, query, binds := armNeedFixtureV1()
	old := view.SecretState.Intent.TrustedUses["KEY"]
	old.Endpoint = "https://old.example/v1/chat/completions"
	view.SecretState.Intent.TrustedUses = map[string]api.TrustedSecretUseV1{"KEY": old}
	view.TrustedReadiness.Unarmed = []string{"KEY"}
	needed, judgeNeed, err := armSecretsNeededV1([]string{"KEY"}, binds, judge, view, query)
	if err != nil || !judgeNeed || !reflect.DeepEqual(needed, []string{"KEY"}) {
		t.Fatal("fresh current-policy negative readiness was replaced by historical tuple authority", err)
	}
}
