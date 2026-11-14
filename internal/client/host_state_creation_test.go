package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func hostCreationRequestFixture() HostCreationRequest {
	return HostCreationRequest{Name: "assigned", Provider: "e2b", CPUs: 2, Memory: 1 << 30, Template: "test-template"}
}

// This generated response tests local reservation semantics. It is not proof
// of a real provider request, machine, SSH connection or helper hello.
func hostCreationResultFixture(request HostCreationRequest) Host {
	return Host{Name: request.Name, Machine: &Machine{Provider: request.Provider, ID: "assigned-test-id"}}
}

func hostCreationPreflightEmpty(t *testing.T, ctx context.Context, name string) {
	t.Helper()
	if err := withHostCreationRemovalInspection(ctx, name, func(view stateFileView) error {
		if view.Exists {
			t.Fatal("the generated fresh-creation fixture unexpectedly has a journal")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHostCreationAssignedResultUsesSameReservationThenNewBaselineObservation(t *testing.T) {
	stateFDTestHome(t)
	request := hostCreationRequestFixture()
	ctx, release, err := BeginHostCreationContext(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	old := ctx.Value(hostStateObservationKey{}).(*HostStateObservation)
	stagesMu.Lock()
	keys := len(old.creation.stage.st.keys)
	stagesMu.Unlock()
	if keys != 0 || old.hasCheckedIdentity() {
		t.Fatal("the empty provider stage claimed checked pin or host identity")
	}
	if _, err := os.Lstat(hostsPath()); !os.IsNotExist(err) {
		t.Fatalf("pre-provider reservation created hosts.toml: %v", err)
	}
	hostCreationPreflightEmpty(t, ctx, request.Name)
	if err := StartHostCreationContext(ctx); err != nil {
		t.Fatal(err)
	}
	response := hostCreationResultFixture(request)
	bound, err := BindHostCreationResultContext(ctx, response)
	if err != nil {
		t.Fatal(err)
	}
	if response.Machine.Created != "" || bound.Machine.Created == "" || !strings.HasSuffix(bound.Machine.Created, "Z") {
		t.Fatal("the client creation stamp mutated the provider object or was not UTC")
	}
	if _, err := time.Parse(time.RFC3339Nano, bound.Machine.Created); err != nil {
		t.Fatal(err)
	}
	if err := UpdateHosts(ctx, HostChange{Kind: "host create", Now: &bound, Pin: true, Tier: TierThrowaway}); err != nil {
		t.Fatal(err)
	}
	saved, err := FindHost(request.Name)
	if err != nil || saved.Machine == nil || saved.Machine.ID != response.Machine.ID || saved.Machine.Created != bound.Machine.Created {
		t.Fatalf("the assigned machine result was not the one saved: %v", err)
	}
	nextStage, abort, err := StagePinOwned(ctx, request.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	next, err := ObserveHostRegistrationContext(nextStage, HostChange{Kind: "host add", Was: &saved, Now: &saved})
	if err != nil {
		t.Fatal(err)
	}
	if next == old || !next.source.exists || !ValidHostID(next.source.generation) || old.source.exists {
		t.Fatal("connect/setup reused the provider reservation instead of a new published-source observation")
	}
}

func TestHostCreationReservationCannotReadWriteOrBindIdentity(t *testing.T) {
	stateFDTestHome(t)
	request := hostCreationRequestFixture()
	ctx, release, err := BeginHostCreationContext(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	h := hostCreationResultFixture(request)
	for _, write := range []bool{false, true} {
		for _, name := range hostStateFiles {
			called := false
			err := withStateFile(ctx, []string{"hosts", HostFileKey(h.Name)}, name, write, func([]byte, bool) ([]byte, error) {
				called = true
				return nil, nil
			})
			if err == nil || called {
				t.Fatalf("creation reservation acquired direct %s JSON authority, write=%v", name, write)
			}
		}
	}
	if err := (&Conn{Host: h, hostID: strings.Repeat("a", 32)}).BindHostStateObservation(ctx); err == nil {
		t.Fatal("creation reservation was upgraded into checked host identity")
	}
	if _, err := os.Lstat(filepath.Join(StateDir(), "hosts", HostFileKey(h.Name))); !os.IsNotExist(err) {
		t.Fatalf("refused creation JSON operations created a host folder: %v", err)
	}
}

func TestHostCreationResultRequiresStartedExactRequestAndSingleResult(t *testing.T) {
	stateFDTestHome(t)
	request := hostCreationRequestFixture()
	ctx, release, err := BeginHostCreationContext(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	response := hostCreationResultFixture(request)
	if _, err := BindHostCreationResultContext(ctx, response); err == nil {
		t.Fatal("a result was bound before the actual provider phase")
	}
	changed := request
	changed.Memory++
	if _, cleanup, err := BeginHostCreationContext(ctx, changed); err == nil {
		cleanup()
		t.Fatal("a changed provider request reused the reservation")
	}
	hostCreationPreflightEmpty(t, ctx, request.Name)
	if err := StartHostCreationContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := StartHostCreationContext(ctx); err == nil {
		t.Fatal("one reservation admitted a second provider attempt")
	}
	bound, err := BindHostCreationResultContext(ctx, response)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := BindHostCreationResultContext(ctx, bound); err != nil || !SameEntry(again, bound) {
		t.Fatalf("exact bound result reuse changed the attempt: %v", err)
	}
	other := cloneObservationHost(bound)
	other.Machine.ID = "another-assigned-id"
	if _, err := BindHostCreationResultContext(ctx, other); err == nil {
		t.Fatal("another machine replaced the once-bound provider response")
	}
}

func TestHostCreationResultRefusesWrongNameProviderAndMissingMachineID(t *testing.T) {
	for _, change := range []string{"name", "provider", "id", "machine"} {
		t.Run(change, func(t *testing.T) {
			stateFDTestHome(t)
			request := hostCreationRequestFixture()
			ctx, release, err := BeginHostCreationContext(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			hostCreationPreflightEmpty(t, ctx, request.Name)
			if err := StartHostCreationContext(ctx); err != nil {
				t.Fatal(err)
			}
			response := hostCreationResultFixture(request)
			switch change {
			case "name":
				response.Name = "foreign"
			case "provider":
				response.Machine.Provider = "foreign"
			case "id":
				response.Machine.ID = ""
			case "machine":
				response.Machine = nil
			}
			if _, err := BindHostCreationResultContext(ctx, response); err == nil {
				t.Fatal("a provider response outside the original request was adopted")
			}
		})
	}
}

func TestHostCreationSourceChangeDoesNotFreshenProviderReply(t *testing.T) {
	stateFDTestHome(t)
	request := hostCreationRequestFixture()
	ctx, release, err := BeginHostCreationContext(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	hostCreationPreflightEmpty(t, ctx, request.Name)
	if err := StartHostCreationContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	response := hostCreationResultFixture(request)
	retained, err := BindHostCreationResultContext(ctx, response)
	if err == nil || retained.Machine == nil || retained.Machine.ID != response.Machine.ID {
		t.Fatal("source expiry refreshed the reply or lost the actual assigned resource ID")
	}
	if err := UpdateHosts(ctx, HostChange{Kind: "host create", Now: &response, Pin: true}); err == nil {
		t.Fatal("an old provider response was published under a fresh source")
	}
}

func TestHostCreationEndedOwnerCannotReserveAgainOrAdoptReply(t *testing.T) {
	stateFDTestHome(t)
	request := hostCreationRequestFixture()
	ctx, release, err := BeginHostCreationContext(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	hostCreationPreflightEmpty(t, ctx, request.Name)
	if err := StartHostCreationContext(ctx); err != nil {
		t.Fatal(err)
	}
	release()
	if _, cleanup, err := BeginHostCreationContext(ctx, request); err == nil {
		cleanup()
		t.Fatal("an ended creation owner reserved again")
	}
	if _, err := BindHostCreationResultContext(ctx, hostCreationResultFixture(request)); err == nil {
		t.Fatal("a late provider response borrowed a new owner")
	}
}
