package client

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestTrustedJudgeGenericAndMemberDTOsCannotSupplyRoles(t *testing.T) {
	for _, raw := range []string{
		`{"protected_trusted_secret_values":{"KEY":"fabricated"}}`,
		`{"trusted_uses":{"KEY":{}}}`,
		`{"secret_delivery":{"trusted_uses":{"KEY":{}}}}`,
		`{"members":[{"id":"child","protected_trusted_secret_values":{"KEY":"fabricated"}}]}`,
		`{"members":[{"id":"child","secret_delivery":{"trusted_uses":{"KEY":{}}}}]}`,
	} {
		if err := checkTrustedJudgeWireFieldsV1([]byte(raw), false); err == nil {
			t.Fatal("generic DTO manufactured trusted delivery")
		}
		if err := beforeTrustedJudgeRequestV1(t.Context(), nil, proto.Request{Op: "arm", Args: []byte(raw)}, map[string]any{}); err == nil {
			t.Fatal("actual physical guard accepted generic trusted delivery")
		}
	}
	if err := checkTrustedJudgeWireFieldsV1([]byte(`{"secret_delivery":{"modes":{"GUEST":"bound"},"binds":{}},"members":[{"id":"child","secrets":{}}]}`), false); err != nil {
		t.Fatal("ordinary guest delivery acquired a trusted-role restriction", err)
	}
	if err := checkTrustedJudgeWireFieldsV1([]byte(`{"members":[{"protected_trusted_secret_values":{}}]}`), true); err == nil {
		t.Fatal("primary argument type authorised a nested member role")
	}
}
