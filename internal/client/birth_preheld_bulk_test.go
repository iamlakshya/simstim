package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func preheldCreationOperationFixture(t *testing.T) (context.Context, func(), HostCreationRequest) {
	t.Helper()
	stateFDTestHome(t)
	req := hostCreationRequestFixture()
	// A genuine native OP is obtained only through an explicit metadata
	// intent; clearing this fixture's observation exposes no JSON authority.
	publication, end, err := withHostPublicationObservation(t.Context(), HostChange{Kind: "outer creation intent", Now: &Host{Name: req.Name}})
	if err != nil {
		t.Fatal(err)
	}
	owned, unlock, err := LockHost(publication, req.Name, "outer creation")
	if err != nil {
		t.Fatal(err)
	}
	end()
	owned = context.WithValue(owned, hostStateObservationKey{}, (*HostStateObservation)(nil))
	return owned, unlock, req
}

func TestCreationAdoptsSamePreheldNativeOperationAndKeepsOuterMetadataRoleSeparate(t *testing.T) {
	outer, unlock, req := preheldCreationOperationFixture(t)
	defer unlock()
	held := outer.Value(heldKey{req.Name}).(*heldLock)
	ctx, release, err := BeginHostCreationContext(outer, req)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	o := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	if o.creation.op.Load() != held {
		t.Fatal("reentrant LockHost did not bind actual original native OP")
	}
	hostCreationPreflightEmpty(t, ctx, req.Name)
	if err := StartHostCreationContext(ctx); err != nil {
		t.Fatal(err)
	}
	bound, err := BindHostCreationResultContext(ctx, hostCreationResultFixture(req))
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateHosts(ctx, HostChange{Kind: "host create", Now: &bound}); err != nil {
		t.Fatal(err)
	}
	EndHostCreationAttemptContext(ctx)
	release()
	if held.gone.Load() {
		t.Fatal("nested creator released the outer physical OP")
	}
	saved, err := FindHost(req.Name)
	if err != nil {
		t.Fatal(err)
	}
	next := cloneObservationHost(saved)
	next.Labels = map[string]string{"setup": "actual outer followup"}
	if err := UpdateHosts(outer, HostChange{Kind: "host add followup", Was: &saved, Now: &next}); err != nil {
		t.Fatalf("outer metadata scope inherited ended creation role: %v", err)
	}
}

func TestCreationRefusesPreheldOperationFromOlderSource(t *testing.T) {
	outer, unlock, req := preheldCreationOperationFixture(t)
	defer unlock()
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, release, err := BeginHostCreationContext(outer, req); err == nil {
		release()
		t.Fatal("preheld OP donated a newer Source to creation")
	}
}

func TestBulkSourceGuardRefusesAfterDuplicatesAndSilentOmission(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", Machine: &Machine{Provider: "lima", ID: "instance", Created: "original", Source: &MachineSourceProvenance{Version: 1, Kind: "lima-plain", ResourceID: "instance", Config: MachineConfigSource{Role: MachineConfigExisting, Path: filepath.Clean(t.TempDir()), ObjectID: "generated-metadata-only"}}}}
	if err := writeHostsFile([]Host{h}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, after := range [][]Host{{h, h}, nil, {{Name: "other"}}, {{Name: h.Name}, {Name: h.Name}}} {
		if err := SaveHosts(t.Context(), after); err == nil {
			t.Fatal("bulk after[] copied/omitted provenance outside actual forget")
		}
		if got, err := os.ReadFile(hostsPath()); err != nil || string(got) != string(before) {
			t.Fatal("refused bulk write changed saved bytes")
		}
	}
}

func TestCurrentCanonicalClonePreventsCallerMutationAtBulkWriter(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", Machine: &Machine{Provider: "lima", ID: "instance", Created: "original", Source: &MachineSourceProvenance{Version: 1, Kind: "lima-plain", ResourceID: "instance", Config: MachineConfigSource{Role: MachineConfigExisting, Path: filepath.Clean(t.TempDir()), ObjectID: "original-object"}}}}
	if err := writeHostsFile([]Host{h}); err != nil {
		t.Fatal(err)
	}
	old := beforeHostsSave
	defer func() { beforeHostsSave = old }()
	beforeHostsSave = func() {
		h.Machine.Source.Config.ObjectID = "caller-changed"
		h.Machine.Source.ResourceID = "caller-changed"
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	saved, err := FindHost(h.Name)
	if err != nil || saved.Machine.Source.ResourceID != "instance" || saved.Machine.Source.Config.ObjectID != "original-object" {
		t.Fatalf("actual frozen bulk snapshot still aliased public provenance: %v", err)
	}
}
