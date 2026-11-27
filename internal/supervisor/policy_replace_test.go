package supervisor

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

func replacementFixture(t *testing.T) (*eventFixture, api.PolicyReplaceArgsV1) {
	t.Helper()
	f := eventController(t)
	level := "process"
	layers := []api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: &level}}}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerSession, Rules: []api.Rule{{Allow: true, Target: "session.example"}}}}
	policy, clean, err := egress.MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}
	f.s.m.Layers, f.s.m.Policy = clean, &policy
	f.s.firstSaved = true
	laptop, _ := api.PolicyLaptopLayersV1(clean)
	hash, _ := api.HashPolicyLaptopLayersV1(laptop)
	return f, api.PolicyReplaceArgsV1{Version: 1, Session: f.s.id, PreviousHash: hash, Layers: laptop}
}

func TestPolicyReplaceConflictNeverPublishesOrSaves(t *testing.T) {
	f, args := replacementFixture(t)
	args.PreviousHash = strings.Repeat("0", 64)
	before := host.CloneManifest(*f.s.m)
	result, err := f.s.replaceLaptopPolicy(args)
	if err != nil || result.State != "conflict" || result.Hash == args.PreviousHash || !reflect.DeepEqual(before, *f.s.m) {
		t.Fatalf("conflict changed policy: %+v, %v", result, err)
	}
	select {
	case <-f.requests:
		t.Fatal("conflict reached egress")
	default:
	}
}

func TestPolicyReplaceAcknowledgesExactHashBeforeDurableAppliedPendingSplit(t *testing.T) {
	f, args := replacementFixture(t)
	next := "microvm"
	args.Layers[0].Sandbox.Wall = &next
	args.Layers[0].Network.Deny = []string{"blocked.example"}
	_, args.Layers, _ = egress.MergeLayers(args.Layers)
	expected, _ := api.HashPolicyLaptopLayersV1(args.Layers)
	oldSave := saveManifest
	var saved *host.Manifest
	saveManifest = func(m *host.Manifest) error { copy := host.CloneManifest(*m); saved = &copy; return nil }
	t.Cleanup(func() { saveManifest = oldSave })
	type outcome struct {
		result api.PolicyReplaceResultV1
		err    *proto.Error
	}
	done := make(chan outcome, 1)
	go func() { result, err := f.s.replaceLaptopPolicy(args); done <- outcome{result, err} }()
	req := f.next(t)
	var update egress.PolicyUpdateArgs
	if json.Unmarshal(req.Args, &update) != nil || len(update.SourceLayers) != 3 || len(update.Policy.Rules) != 2 {
		t.Fatal("replacement lost complete sources or live lease projection")
	}
	if saved != nil {
		t.Fatal("replacement saved before egress acknowledged")
	}
	raw, _ := json.Marshal(map[string]string{"hash": expected})
	if err := f.writer.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true, Result: raw}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.result.State != "applied" || got.result.Hash != expected || len(got.result.NextStart) != 1 || got.result.NextStart[0] != "sandbox.wall" {
			t.Fatalf("replacement = %+v, %v", got.result, got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("replacement did not finish")
	}
	if saved == nil || saved.Policy.Sandbox.Wall == nil || *saved.Policy.Sandbox.Wall != "process" || len(saved.PolicyNext) == 0 || len(saved.Layers[2].Rules) != 1 || saved.Layers[2].Rules[0].Target != "session.example" {
		t.Fatal("durable publication changed applied level or excluded session source")
	}
	view, err := host.PolicySnapshotWithNext(saved.Layers, saved.PolicyNext)
	if err != nil || view.LayersHash != expected {
		t.Fatal("next read lost acknowledged desired hash", err)
	}
}
