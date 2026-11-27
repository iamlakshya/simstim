package client

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type machineBirthFixtureKey struct{}

type machineBirthFixtureFrame struct {
	ctx     context.Context
	scope   *HostCreationSourceScope
	request HostCreationRequest
	result  Host
	source  *MachineSourceProvenance
	calls   int
}

// Generated observer tests ONLY client reservation/copy/commit boundaries.
// It runs no provider and proves no native engine/configuration birth facts.
// Actual Docker/Lima private-frame/native-pin tests own that provenance.
func generatedMachineBirthObserver(ctx context.Context, scope *HostCreationSourceScope, result Host) (*MachineSourceProvenance, error) {
	frame, _ := ctx.Value(machineBirthFixtureKey{}).(*machineBirthFixtureFrame)
	if frame == nil || frame.ctx != ctx || frame.scope != scope || frame.request != scope.Request() || !SameEntry(frame.result, result) {
		return nil, machineSourceUnknown()
	}
	if err := scope.CheckContext(ctx); err != nil {
		return nil, err
	}
	frame.calls++
	return CloneMachineSource(frame.source), scope.CheckContext(ctx)
}

func machineBirthRegistryFixture(t *testing.T) {
	t.Helper()
	hostMachineSourceObservers.Lock()
	oldStarted, oldValues := hostMachineSourceObservers.started, hostMachineSourceObservers.values
	hostMachineSourceObservers.started, hostMachineSourceObservers.values = false, make(map[string]HostMachineSourceObserver)
	hostMachineSourceObservers.Unlock()
	t.Cleanup(func() {
		hostMachineSourceObservers.Lock()
		hostMachineSourceObservers.started, hostMachineSourceObservers.values = oldStarted, oldValues
		hostMachineSourceObservers.Unlock()
	})
	if err := RegisterHostMachineSourceObserver("lima", generatedMachineBirthObserver); err != nil {
		t.Fatal(err)
	}
}

func machineBirthFrameFixture(t *testing.T) (context.Context, *machineBirthFixtureFrame) {
	t.Helper()
	stateFDTestHome(t)
	machineBirthRegistryFixture(t)
	request := HostCreationRequest{Name: "birth", Provider: "lima", CPUs: 2}
	ctx, release, err := BeginHostCreationContext(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	hostCreationPreflightEmpty(t, ctx, request.Name)
	if err := StartHostCreationContext(ctx); err != nil {
		t.Fatal(err)
	}
	scope, err := HostCreationSourceScopeContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	frame := &machineBirthFixtureFrame{scope: scope, request: request, result: Host{Name: request.Name, Machine: &Machine{Provider: request.Provider, ID: "created-instance"}},
		source: &MachineSourceProvenance{Version: MachineSourceVersion, Kind: "lima-plain", ResourceID: "created-instance",
			Config: MachineConfigSource{Role: MachineConfigExisting, Path: filepath.Clean(t.TempDir()), ObjectID: "generated-metadata-id"}}}
	frame.ctx = context.WithValue(ctx, machineBirthFixtureKey{}, frame)
	return frame.ctx, frame
}

func TestMachineSourceBirthUsesActualFrameThenConsumesOneCommit(t *testing.T) {
	ctx, frame := machineBirthFrameFixture(t)
	if _, err := HostCreationSourceScopeContext(ctx); err == nil {
		t.Fatal("a duplicate provider entry could run commands under the old reservation")
	}
	result, err := RecordHostMachineSourceBirthContext(ctx, frame.result)
	if err != nil || result.machineSourceBirth == nil || frame.calls != 1 {
		t.Fatalf("actual generated frame did not produce one private token: %v", err)
	}
	// Reconstructing only public data is not an opaque proof.
	raw := Host{Name: result.Name, Machine: &Machine{Provider: result.Machine.Provider, ID: result.Machine.ID, Source: CloneMachineSource(result.Machine.Source)}}
	if _, err := BindHostCreationResultContext(ctx, raw); err == nil {
		t.Fatal("a reconstructed public result borrowed the creation token")
	}
	bound, err := BindHostCreationResultContext(ctx, result)
	if err != nil {
		t.Fatal(err)
	}
	if err := UpdateHosts(ctx, HostChange{Kind: "host create", Now: &bound, Tier: TierShared}); err != nil {
		t.Fatal(err)
	}
	if !result.machineSourceBirth.consumed {
		t.Fatal("the successful same writer did not consume its original birth token")
	}
	saved, err := FindHost(bound.Name)
	if err != nil || saved.machineSourceBirth != nil || !reflect.DeepEqual(saved.Machine.Source, bound.Machine.Source) {
		t.Fatalf("saved metadata lost data or persisted authority: %v", err)
	}
	if _, err := RecordHostMachineSourceBirthContext(ctx, frame.result); err == nil || frame.calls != 1 {
		t.Fatal("a consumed/copy result replayed the builtin observer")
	}
}

func TestMachineSourceBirthRefusesRawCopiedOrCrossProviderFrames(t *testing.T) {
	for _, kind := range []string{"no-frame", "copied-context", "other-scope", "other-provider", "changed-result"} {
		t.Run(kind, func(t *testing.T) {
			ctx, frame := machineBirthFrameFixture(t)
			result := cloneObservationHost(frame.result)
			switch kind {
			case "no-frame":
				ctx = context.WithValue(ctx, machineBirthFixtureKey{}, (*machineBirthFixtureFrame)(nil))
			case "copied-context":
				ctx = context.WithValue(ctx, struct{ copied bool }{}, true)
			case "other-scope":
				frame.scope = &HostCreationSourceScope{}
			case "other-provider":
				result.Machine.Provider = "docker"
			case "changed-result":
				result.Machine.ID = "other-instance"
			}
			if h, err := RecordHostMachineSourceBirthContext(ctx, result); err == nil || h.machineSourceBirth != nil || frame.calls != 0 {
				t.Fatal("a DTO/copy/cross-provider frame minted birth provenance")
			}
		})
	}
}

func TestMachineSourceObserverRegistrationIsFixedAndBeforeRequests(t *testing.T) {
	machineBirthRegistryFixture(t)
	if err := RegisterHostMachineSourceObserver("lima", generatedMachineBirthObserver); err == nil {
		t.Fatal("a fixed builtin observer was replaced")
	}
	if err := RegisterHostMachineSourceObserver("other", generatedMachineBirthObserver); err == nil {
		t.Fatal("an arbitrary provider could install an observer")
	}
	freezeHostMachineSourceObservers()
	if err := RegisterHostMachineSourceObserver("docker", generatedMachineBirthObserver); err == nil {
		t.Fatal("request-time observer installation was accepted")
	}
}

func TestGenericHostWritersCannotMintOrRetargetMachineSource(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", Machine: &Machine{Provider: "lima", ID: "instance", Created: "original", Source: &MachineSourceProvenance{
		Version: MachineSourceVersion, Kind: "lima-plain", ResourceID: "instance", Config: MachineConfigSource{Role: MachineConfigExisting, Path: filepath.Clean(t.TempDir()), ObjectID: "generated-metadata-id"}}}}
	if err := SaveHosts(t.Context(), []Host{h}); err == nil {
		t.Fatal("bulk DTO introduced a new provenance record")
	}
	if err := SaveHost(h); err == nil {
		t.Fatal("generic save introduced a new provenance record")
	}
	// Install generated already-saved data only to test preservation/CAS.
	// This private test writer is not a production provenance constructor.
	if err := writeHostsFile([]Host{h}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"resource", "locator", "identity", "clear"} {
		next := cloneObservationHost(h)
		switch kind {
		case "resource":
			next.Machine.Source.ResourceID = "other"
		case "locator":
			next.Machine.Source.Config.ObjectID = "other"
		case "identity":
			next.Machine.ID = "other"
		case "clear":
			next.Machine.Source = nil
		}
		if err := UpdateHosts(t.Context(), HostChange{Kind: "generic", Was: &h, Now: &next}); err == nil {
			t.Fatalf("generic update changed provenance binding: %s", kind)
		}
	}
	saved, err := FindHost(h.Name)
	if err != nil || !SameEntry(saved, h) {
		t.Fatalf("refused provenance writes changed the current entry: %v", err)
	}
}

func TestTypedHostWriteRefusesFutureProvenanceInsteadOfDroppingIt(t *testing.T) {
	stateFDTestHome(t)
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte("# future source record\n[[host]]\nname='box'\n[host.machine]\nprovider='lima'\nid='instance'\n[host.machine.source]\nversion=2\nkind='future'\nresource_id='instance'\nfuture_field={nested=[1,2]}\n")
	if err := os.WriteFile(hostsPath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	hosts, err := LoadHosts()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), hosts); err == nil {
		t.Fatal("typed writer discarded an unsupported source domain")
	}
	if got, err := os.ReadFile(hostsPath()); err != nil || string(got) != string(raw) {
		t.Fatal("future source bytes were not preserved on refusal")
	}
}
