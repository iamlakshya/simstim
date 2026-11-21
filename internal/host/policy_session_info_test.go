package host

import (
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

func TestPolicySessionInfoKeepsRunningLevelAndProjectsOnlyBoundedPendingData(t *testing.T) {
	level, next := "process", "microvm"
	layers := []api.Layer{{Kind: api.LayerAll, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: &level}}}, {Kind: api.LayerHost, Name: "box"}, {Kind: api.LayerSession}}
	_, layers, err := egress.MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}
	laptop, _ := api.PolicyLaptopLayersV1(layers)
	laptop[0].Sandbox.Wall = &next
	raw, err := api.EncodePolicyNextLayersV1(laptop)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manifest{Layers: layers, PolicyNext: raw, Wall: &WallRecord{Level: level}}
	info := SessionInfo{Wall: &api.WallInfo{Level: level}}
	policySessionInfo(m, &info)
	hash, _ := api.HashPolicyLaptopLayersV1(laptop)
	if info.LayersHash != hash || info.PolicyNextWall != next || len(info.PolicyNextStart) != 1 || info.Wall.Level != level || m.Wall.Level != level {
		t.Fatal("pending display replaced actual running level", info)
	}
	m.PolicyNext = []byte(`{"v":2,"future":true}`)
	info = SessionInfo{}
	policySessionInfo(m, &info)
	if info.LayersHash != "" || info.PolicyNextWall != "" || len(info.PolicyNextStart) != 0 {
		t.Fatal("future pending capsule fabricated a supported projection")
	}
}
