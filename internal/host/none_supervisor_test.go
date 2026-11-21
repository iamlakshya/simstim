package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestNoneCarriersCannotBecomeSandboxControls(t *testing.T) {
	none := "none"
	args := NewArgs{Wall: "none", Layers: []api.Layer{{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: &none}}}}}
	if err := checkNonePolicyCarriersV1(args); err != nil {
		t.Fatal(err)
	}
	bound := "locked"
	args.Layers[0].Sandbox.Preset = &bound
	if err := checkNonePolicyCarriersV1(args); err == nil {
		t.Fatal("a sandbox preset was silently treated as an unwalled control")
	}
	args.Layers = nil
	args.Mounts = []string{"/tmp/data:/mnt/data:ro"}
	if err := checkNonePolicyCarriersV1(args); err == nil {
		t.Fatal("an unwalled start accepted a mount plan")
	}
}

func TestNoneMigrationCannotComeFromSavedMetadata(t *testing.T) {
	before := &Manifest{ID: "a2a2a2a2", Run: Runtime{BootID: strings.Repeat("a", 32)}}
	next := CloneManifest(*before)
	next.Wall = &WallRecord{Level: "none", Driver: "none"}
	var absent *legacyNoneMigrationV1
	if err := absent.check(context.Background(), &SessionOperation{}, before, &next); err == nil {
		t.Fatal("saved none metadata issued a legacy path transition")
	}
	zero := &legacyNoneMigrationV1{before: *before, boot: BootID()}
	if err := zero.check(context.Background(), &SessionOperation{}, before, &next); err == nil {
		t.Fatal("a copied boot ID issued a legacy path transition")
	}
}

func TestNoneStillNeedsExplicitFloorDowngrade(t *testing.T) {
	if err := validateChosenWall(NewArgs{Wall: "none", Floor: "process"}); err == nil {
		t.Fatal("the new runtime bypassed the original wall floor")
	}
	if err := validateChosenWall(NewArgs{Wall: "none", Floor: "process", Downgrade: true, DowngradedFrom: "process"}); err != nil {
		t.Fatal(err)
	}
}

func TestNoneCanonicalShapeKeepsApprovedCreationFloorAndRefusesWallRoles(t *testing.T) {
	base := Manifest{ID: "a2a2a2a2", Wall: &WallRecord{Level: "none", Driver: "none", Floor: "process", DowngradedFrom: "process"}}
	if !NoneSupervisorShapeV1(&base) {
		t.Fatal("a genuine approved creation downgrade was disabled")
	}
	none := "none"
	base.Layers = []api.Layer{{Kind: api.LayerSession, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: &none}}}}
	if !NoneSupervisorShapeV1(&base) {
		t.Fatal("harmless selector carriers were treated as applied sandbox policy")
	}
	for name, change := range map[string]func(*Manifest){
		"unapproved-floor": func(m *Manifest) { m.Wall.DowngradedFrom = "" },
		"runtime":          func(m *Manifest) { m.VMNeedV1 = json.RawMessage(`{"v":1}`) },
		"agents":           func(m *Manifest) { m.Agents = "disk" },
		"policy":           func(m *Manifest) { m.Policy = &api.Policy{} },
		"policy-next":      func(m *Manifest) { m.PolicyNext = json.RawMessage(`{}`) },
		"armed":            func(m *Manifest) { m.Armed = true },
		"capture":          func(m *Manifest) { m.VMCaptureNonce = "unexpected" },
		"image":            func(m *Manifest) { m.Run.Image = "unexpected" },
		"artefact":         func(m *Manifest) { m.Run.Artefacts = []string{"unexpected"} },
		"slot-account":     func(m *Manifest) { m.Run.NativeAccountMode = "vm" },
		"slot-generation":  func(m *Manifest) { m.Run.NativeAccountGeneration = "unexpected" },
		"foreign-unit":     func(m *Manifest) { m.Run.Unit = "foreign.service" },
		"unknown-role":     func(m *Manifest) { m.extra = map[string]json.RawMessage{"future_execution": json.RawMessage(`{}`)} },
		"unknown-runtime":  func(m *Manifest) { m.Run.extra = map[string]json.RawMessage{"future_execution": json.RawMessage(`{}`)} },
		"layer-mount":      func(m *Manifest) { m.Layers[0].Sandbox.Mounts = []string{"/tmp:/work:ro"} },
	} {
		t.Run(name, func(t *testing.T) {
			m := CloneManifest(base)
			change(&m)
			if NoneSupervisorShapeV1(&m) {
				t.Fatal("a conflicting execution role was accepted")
			}
		})
	}
}

func TestNoneResumeRefreshKeepsExactlyItsOriginalApprovedFloor(t *testing.T) {
	process := "process"
	fresh := []api.Layer{{Kind: api.LayerHost, LayerValues: api.LayerValues{Sandbox: api.SandboxValues{Wall: &process}}}}
	m := &Manifest{ID: "a2a2a2a2", Wall: &WallRecord{Level: "none", Driver: "none", Floor: "process", DowngradedFrom: "process"}}
	if err := ApplyResumeLayers(m, fresh); err != nil {
		t.Fatal("the unchanged original floor was refused", err)
	}
	if m.Policy != nil || len(m.Layers) != 0 || m.Wall.Level != "none" || m.Wall.DowngradedFrom != "process" {
		t.Fatal("harmless selectors applied policy or changed the original approval")
	}
	stronger := "gvisor"
	fresh[0].Sandbox.Wall = &stronger
	if err := ApplyResumeLayers(m, fresh); err == nil {
		t.Fatal("a new stronger selector silently extended the original downgrade")
	}
	m.Wall.Floor, m.Wall.DowngradedFrom = "", ""
	fresh[0].Sandbox.Wall = &process
	if err := ApplyResumeLayers(m, fresh); err == nil {
		t.Fatal("a fresh source issued a downgrade absent from the original creation")
	}
}
