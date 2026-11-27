package client

import (
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestCreationCopyResourceRetainsTrustedLocalPrecedence(t *testing.T) {
	local, session := "512M", "64M"
	args := NewArgs{Layers: []api.Layer{
		{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{CPMax: &local}}},
		{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{CPMax: &session}}},
	}}
	raw, err := encodeLocalPolicySources(args)
	if err != nil {
		t.Fatal(err)
	}
	local, session = "2G", "1G"
	pin, err := api.DecodePolicySourcePin(raw)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Resources.CPMax == nil || *pin.Resources.CPMax != 64<<20 {
		t.Fatal("later settings mutation changed the original local copy ceiling")
	}
}

func TestRepositoryFileCannotSelectCopyResources(t *testing.T) {
	for _, checked := range []string{"64M", "1G"} {
		local := "512M"
		args := NewArgs{Layers: []api.Layer{
			{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{CPMax: &local}}},
			{Kind: api.LayerSession},
		}, RepositoryPolicy: &api.RepositoryPolicy{Layer: api.Layer{Kind: api.LayerRepo, Name: "repo", Source: api.SourceRepositoryFile,
			LayerValues: api.LayerValues{Sandbox: api.SandboxValues{CPMax: &checked}}}}}
		if raw, err := encodeLocalPolicySources(args); err == nil || raw != nil {
			t.Fatalf("repository file selected a copy resource ceiling %q", checked)
		}
	}
}

func TestCopyLimitRejectsOriginalPinWithoutDeliveryAcknowledgement(t *testing.T) {
	// The generated transport stamp isolates the actual local accessor. It
	// neither starts a provider nor proves native SSH or helper provenance.
	h := h5GrantState(t)
	policy := h5Policy()
	raw, err := encodeLocalPolicySources(NewArgs{Layers: []api.Layer{{Kind: api.LayerSession}}})
	if err != nil {
		t.Fatal(err)
	}
	policy.PolicySources = raw
	if err := reserveSandbox(t.Context(), h, policy); err != nil {
		t.Fatal(err)
	}
	connection := finishGeneratedTransportFixture(t, h, policy.HostID)
	operation, err := openSettingsOperation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer operation.Close()
	original, err := getSandboxReceiptPolicyContext(t.Context(), connection, policy.SessionID)
	if err != nil || original == nil || original.deliveryReceipt != nil || len(original.PolicySources) == 0 {
		t.Fatalf("pin-only fixture = %+v %v", original, err)
	}
	var refused *UserError
	limit, err := connection.copyLimit(t.Context(), policy.SessionID, &api.Policy{Preset: api.PresetBalanced}, operation)
	if limit != 0 || !errors.As(err, &refused) || refused.Code != "cp_policy" {
		t.Fatalf("unacknowledged local pin granted a copy ceiling: %d %v", limit, err)
	}
}
