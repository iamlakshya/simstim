package client

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/proto"
)

func policyReplacementConnection(t *testing.T, replace func(proto.Request) (any, *proto.Error)) (*Conn, *runtimeDeliveryPeer, *PolicyReplacement, context.Context) {
	t.Helper()
	var snapshot api.Policy
	connection, peer, h := heldRequestConnectionFixture(t, func(req proto.Request) (any, *proto.Error) {
		if req.Op == "policy.ls" {
			return snapshot, nil
		}
		return replace(req)
	})
	connection.Features = append(connection.Features, "policy", PolicyLayerFeature, api.FeaturePolicyLiveV1, api.FeatureHTTPMethods)
	layers, err := ResolvePolicyLayers(Settings{}, h, "", "", api.LayerValues{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err = egress.MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Sources = api.CloneLayers(layers)
	snapshot.LayersHash, err = api.HashPolicyLaptopLayersV1(layers)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeLocalPolicySources(NewArgs{Layers: layers})
	if err != nil {
		t.Fatal(err)
	}
	creation := h5Policy()
	creation.SessionID = "root2345"
	creation.Ceiling = TierOwn
	creation.PolicySources = raw
	creation.Secrets = nil
	if err := reserveSandbox(t.Context(), h, creation); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath(), []byte("[network]\ndeny=['blocked.example']\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r := NewPolicyReplaceGrantRequest(creation.SessionID)
	ctx := WithGrantRequest(t.Context(), r)
	t.Cleanup(func() { _ = r.Close() })
	plan, err := connection.PlanPolicyReplacement(ctx, creation.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	return connection, peer, plan, ctx
}

func TestPolicyReplacementRetriesOnlyExplicitConflictsOnOriginalConnection(t *testing.T) {
	var connection *Conn
	attempts := 0
	c, peer, plan, ctx := policyReplacementConnection(t, func(req proto.Request) (any, *proto.Error) {
		attempts++
		var args api.PolicyReplaceArgsV1
		if req.Op != "policy.replace" || json.Unmarshal(req.Args, &args) != nil || args.Session != "root2345" || args.Validate() != nil {
			t.Errorf("invalid actual replacement frame: %s", req.Args)
		}
		responseCtx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_, release, err := LockHost(responseCtx, connection.Host.Name, "replacement response")
		if err != nil {
			t.Error("replacement retained host publication during ACK", err)
		} else {
			release()
		}
		if attempts == 1 {
			return api.PolicyReplaceResultV1{Version: 1, Hash: strings.Repeat("b", 64), State: "conflict"}, nil
		}
		if args.PreviousHash != strings.Repeat("b", 64) {
			t.Error("retry did not use the explicit conflict's current hash")
		}
		hash, _ := api.HashPolicyLaptopLayersV1(args.Layers)
		return api.PolicyReplaceResultV1{Version: 1, Hash: hash, State: "applied"}, nil
	})
	connection = c
	change, err := BeginPolicyChange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer change.Close()
	ctx = change.Context(ctx)
	result, err := plan.Apply(ctx)
	if err != nil || result.State != "applied" || attempts != 2 || peer.count("policy.replace", "") != 2 {
		t.Fatalf("actual source-bound conflict retry = %+v %v attempts=%d", result, err, attempts)
	}
}

func TestPolicyReplacementRefusesChangedSettingsSourceBeforeMutation(t *testing.T) {
	_, peer, plan, ctx := policyReplacementConnection(t, func(req proto.Request) (any, *proto.Error) { return nil, unexpectedRuntimeRequest(req) })
	old, err := os.ReadFile(settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(ConfigDir(), "replacement.toml")
	if err := os.WriteFile(replacement, old, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, settingsPath()); err != nil {
		t.Fatal(err)
	}
	change, err := BeginPolicyChange(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer change.Close()
	if _, err := plan.Apply(change.Context(ctx)); err == nil || peer.count("policy.replace", "") != 0 {
		t.Fatal("equal text from a new settings inode refreshed old policy authority", err)
	}
}

func TestPolicyReplacementCannotUseGenericCallOrAnotherRequest(t *testing.T) {
	c, peer, plan, ctx := policyReplacementConnection(t, func(req proto.Request) (any, *proto.Error) { return nil, unexpectedRuntimeRequest(req) })
	args := api.PolicyReplaceArgsV1{Version: 1, Session: "root2345", PreviousHash: strings.Repeat("a", 64), Layers: []api.Layer{{Kind: api.LayerAll}}}
	if err := c.Call(ctx, "policy.replace", args, nil); err == nil || peer.count("policy.replace", "") != 0 {
		t.Fatal("generic Call bypassed the private original body guard", err)
	}
	other := NewPolicyReplaceGrantRequest("root2345")
	defer other.Close()
	if err := plan.Check(WithGrantRequest(t.Context(), other)); err == nil {
		t.Fatal("another same-session request borrowed the plan")
	}
}

func TestPolicyPendingStateRefusesUnknownMetadataWithoutOverwritingIt(t *testing.T) {
	isolateH5GrantHome(t)
	if err := updatePolicyPending(t.Context(), nil, []string{"second", "first"}); err != nil {
		t.Fatal(err)
	}
	state, err := readPolicyPending(t.Context())
	if err != nil || strings.Join(state.Hosts, ",") != "first,second" {
		t.Fatal("pending state order", state, err)
	}
	path := filepath.Join(StateDir(), policyPendingFile)
	raw := []byte(`{"v":2,"hosts":["first"],"future":{"retain":true}}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := updatePolicyPending(t.Context(), []string{"first"}, nil); err == nil {
		t.Fatal("future pending state was rewritten")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(raw) {
		t.Fatal("failed pending write lost opaque state", err)
	}
}

func TestPolicyLearningCountsEqualFlowsAndNeverInfersProvenanceFromRulePrefix(t *testing.T) {
	counts := map[string]uint64{}
	flow := api.NetAuditEntry{Kind: "net", Host: "api.example", Port: 443, Decision: "allow-unknown", Rule: "unknown:allow"}
	fake := flow
	fake.Decision = "allow"
	fake.Rule = "classify:claimed"
	human, judge := flow, flow
	human.Decision, human.Rule, human.By = "allow", "g234567", "live"
	judge.Decision, judge.Rule, judge.By = "allow", "g345678", "classifier"
	masked := flow
	masked.Host = "private target"
	if err := addPolicyLearnEntries(counts, []api.NetAuditEntry{flow, flow, human, judge, fake, masked}); err != nil || len(counts) != 1 || counts["api.example:443"] != 4 {
		t.Fatal("learned a mask/claimed label or dropped equal flows", counts, err)
	}
}
