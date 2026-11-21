package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

// This fake keeps each actual provider operation distinct from its reporting
// Host. Tests never reconstruct a creation resource from saved metadata.
type sessionMachineResourceFixtureV1 struct {
	host               Host
	lifecycle          *sessionMachineLifecycleFixtureV1
	rollbackErr        error
	rollbacks, closes  int
	rollbackContext    context.Context
	rollbackContextErr error
}

func (r *sessionMachineResourceFixtureV1) Host() Host           { return cloneObservationHost(r.host) }
func (r *sessionMachineResourceFixtureV1) Lifecycle() Lifecycle { return r.lifecycle }
func (r *sessionMachineResourceFixtureV1) Rollback(ctx context.Context) error {
	r.rollbacks++
	r.rollbackContext = ctx
	r.rollbackContextErr = ctx.Err()
	return r.rollbackErr
}
func (r *sessionMachineResourceFixtureV1) Close() error { r.closes++; return nil }

type sessionMachineLifecycleFixtureV1 struct {
	mu           sync.Mutex
	states       []string
	calls        []string
	destroyErr   error
	afterDestroy func()
}

func (l *sessionMachineLifecycleFixtureV1) record(call string) {
	l.mu.Lock()
	l.calls = append(l.calls, call)
	l.mu.Unlock()
}
func (l *sessionMachineLifecycleFixtureV1) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}
func (l *sessionMachineLifecycleFixtureV1) Status(context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, "status")
	state := MachineRunning
	if len(l.states) != 0 {
		state = l.states[0]
		if len(l.states) > 1 {
			l.states = l.states[1:]
		}
	}
	return state, nil
}
func (l *sessionMachineLifecycleFixtureV1) Start(context.Context) error {
	l.record("start")
	return nil
}
func (l *sessionMachineLifecycleFixtureV1) Stop(context.Context) error { l.record("stop"); return nil }
func (l *sessionMachineLifecycleFixtureV1) Destroy(context.Context) error {
	l.record("destroy")
	if l.afterDestroy != nil {
		l.afterDestroy()
	}
	return l.destroyErr
}
func (*sessionMachineLifecycleFixtureV1) Transport(context.Context, bool) (Transport, error) {
	return nil, errors.New("the fixture uses the explicit real-protocol bridge route")
}

func beginSessionMachineFixtureV1(t *testing.T, downgrade bool) (*SessionMachineOwnerV1, *sessionMachineResourceFixtureV1) {
	t.Helper()
	stateFDTestHome(t)
	request := HostCreationRequest{Name: "m-work", Provider: "e2b"}
	resource := &sessionMachineResourceFixtureV1{host: Host{Name: request.Name, Machine: &Machine{Provider: "e2b", ID: "created-sandbox", User: "user"}}, lifecycle: &sessionMachineLifecycleFixtureV1{}}
	called := 0
	owner, err := BeginSessionMachineV1(t.Context(), request, "work", downgrade, func(ctx context.Context) (Host, SessionMachineResourceV1, error) {
		called++
		if err := CheckSessionMachineCreationContextV1(ctx, request); err != nil {
			return Host{}, nil, err
		}
		return resource.Host(), resource, nil
	})
	if err != nil || owner == nil || called != 1 {
		t.Fatalf("owned creation = %v, %v, calls %d", owner, err, called)
	}
	registerSessionMachineFixtureV1(t, owner)
	t.Cleanup(func() {
		if err := owner.Finish(t.Context()); err != nil {
			t.Error(err)
		}
	})
	return owner, resource
}

func TestSessionMachineCreationSavesSharedOwnershipAndRollsBackOnlyItsResource(t *testing.T) {
	owner, resource := beginSessionMachineFixtureV1(t, false)
	host := owner.Host()
	if host.Name != "m-work" || host.Machine == nil || host.Machine.ID != "created-sandbox" || host.Machine.Session != "work" || host.Machine.SessionID != "" || host.Machine.Created == "" {
		t.Fatalf("saved machine = %+v", host)
	}
	tier, _, err := HostGrantTier(t.Context(), host)
	if err != nil || tier != TierShared {
		t.Fatalf("provider tier = %v, %v", tier, err)
	}
	if owner.Target() == nil || owner.Target().machineOwner != owner {
		t.Fatal("creation writer lost its original owner")
	}
	if err := owner.Commit(t.Context()); err == nil {
		t.Fatal("a saved host row committed an unacknowledged session")
	}
	if err := owner.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if resource.rollbacks != 1 || resource.closes != 1 {
		t.Fatalf("resource cleanup = %d rollbacks, %d closes", resource.rollbacks, resource.closes)
	}
	if _, err := FindHost(host.Name); err == nil {
		t.Fatal("successful rollback kept the saved host")
	}
	if err := owner.Finish(t.Context()); err != nil || resource.rollbacks != 1 || resource.closes != 1 {
		t.Fatal("repeated Finish replayed the provider capability")
	}
}

func TestSessionMachineFailedRollbackRetainsSavedHostAndOriginalCleanupContext(t *testing.T) {
	owner, resource := beginSessionMachineFixtureV1(t, false)
	resource.rollbackErr = errors.New("original provider refused deletion")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := owner.Finish(ctx)
	var user *UserError
	if !errors.As(err, &user) || user.Code != "machine_cleanup" || !strings.Contains(user.Hint, "host rm m-work --destroy") {
		t.Fatalf("cleanup failure = %v", err)
	}
	if resource.rollbackContext == nil || resource.rollbackContextErr != nil {
		t.Fatal("rollback inherited cancellation instead of retaining bounded cleanup")
	}
	if _, hasDeadline := resource.rollbackContext.Deadline(); !hasDeadline {
		t.Fatal("rollback lost its absolute cleanup budget")
	}
	if resource.rollbacks != 1 || resource.closes != 1 {
		t.Fatal("failed rollback did not close its original handle")
	}
	if host, err := FindHost("m-work"); err != nil || !IsSessionMachineV1(host) {
		t.Fatalf("failed rollback lost its recovery row: %+v, %v", host, err)
	}
}

func TestSessionMachineCreationFailureBeforeSaveUsesActualReturnedHandle(t *testing.T) {
	stateFDTestHome(t)
	request := HostCreationRequest{Name: "m-work", Provider: "e2b"}
	resource := &sessionMachineResourceFixtureV1{host: Host{Name: request.Name, Machine: &Machine{Provider: "e2b", ID: "created-sandbox"}}, lifecycle: &sessionMachineLifecycleFixtureV1{}}
	failure := errors.New("original source changed after the provider reply")
	owner, err := BeginSessionMachineV1(t.Context(), request, "work", false, func(ctx context.Context) (Host, SessionMachineResourceV1, error) {
		if err := CheckSessionMachineCreationContextV1(ctx, request); err != nil {
			return Host{}, nil, err
		}
		return resource.Host(), resource, failure
	})
	if !errors.Is(err, failure) || owner == nil {
		t.Fatalf("partial creation = %v, %v", owner, err)
	}
	if err := owner.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if resource.rollbacks != 1 || resource.closes != 1 {
		t.Fatal("the actual post-create failure handle was discarded")
	}
	if _, err := FindHost(request.Name); err == nil {
		t.Fatal("a failed producer published a host row")
	}
}

func TestSessionMachineSaveFailureRollsBackActualCreation(t *testing.T) {
	stateFDTestHome(t)
	request := HostCreationRequest{Name: "m-work", Provider: "e2b"}
	resource := &sessionMachineResourceFixtureV1{host: Host{Name: request.Name, Machine: &Machine{Provider: "e2b", ID: "created-sandbox"}}, lifecycle: &sessionMachineLifecycleFixtureV1{}}
	failure := errors.New("generated host save failure")
	previous := writeHosts
	writeHosts = func([]Host) error { return failure }
	owner, err := BeginSessionMachineV1(t.Context(), request, "work", false, func(context.Context) (Host, SessionMachineResourceV1, error) { return resource.Host(), resource, nil })
	writeHosts = previous
	if err == nil || owner == nil {
		t.Fatalf("failed save = %v, %v", owner, err)
	}
	if err := owner.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if resource.rollbacks != 1 || resource.closes != 1 {
		t.Fatal("failed save reconstructed or discarded its provider cleanup")
	}
	if _, err := FindHost(request.Name); err == nil {
		t.Fatal("failed save retained a phantom recovery row")
	}
}

func TestSessionMachineSharedProcessPreparationNeedsOriginalDowngradeOwner(t *testing.T) {
	for _, downgrade := range []bool{false, true} {
		t.Run(fmt.Sprint(downgrade), func(t *testing.T) {
			owner, _ := beginSessionMachineFixtureV1(t, downgrade)
			target := owner.Target()
			if err := allowSandboxPrepareFloorV1(target, "process"); (err == nil) != downgrade {
				t.Fatalf("machine process floor with downgrade=%v: %v", downgrade, err)
			}
			ordinary, err := OpenHostPrepareTargetV1(t.Context(), target.Name())
			if err != nil {
				t.Fatal(err)
			}
			if err := allowSandboxPrepareFloorV1(ordinary, "process"); err == nil {
				t.Fatal("saved machine metadata exempted ordinary host preparation")
			}
			if err := allowSandboxPrepareFloorV1(target, "none"); err == nil {
				t.Fatal("the process downgrade exception widened to none")
			}
			if _, err := ChooseWall(target.Host(), TierShared, "process", false); err == nil {
				t.Fatal("shared machine silently selected process without --downgrade")
			}
			choice, err := ChooseWall(target.Host(), TierShared, "process", true)
			if err != nil || choice.DowngradedFrom != "gvisor" {
				t.Fatalf("explicit machine downgrade = %+v, %v", choice, err)
			}
			plain := target.Host()
			plain.Machine = nil
			if _, err := ChooseWall(plain, TierShared, "process", true); err == nil {
				t.Fatal("machine exception widened to an ordinary shared host")
			}
		})
	}
}

func TestSessionMachineSecretsRequireSharedMarkAndExposureBeforeValueCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the secret marker command uses the POSIX shell")
	}
	for _, test := range []struct {
		name, mark string
		expose     bool
		code       string
	}{
		{"unmarked", "", true, "tier_refused"},
		{"throwaway", "throwaway", true, "tier_refused"},
		{"shared without exposure", "shared", false, "machine_secret_exposure"},
		{"shared with exposure", "shared", true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := Host{Name: "m-work", Machine: &Machine{Provider: "e2b", ID: "created-sandbox", Session: "work"}}
			prepareSavedFixture(t, host, TierShared, strings.Repeat("6", 32), false)
			marker := filepath.Join(t.TempDir(), "value-command-ran")
			config := fmt.Sprintf("[secrets]\nAPI_KEY = %q\n", "touch "+ShellWord(marker)+"; printf generated-session-value")
			if test.mark != "" {
				config += fmt.Sprintf("[secret_tiers]\nAPI_KEY = %q\n", test.mark)
			}
			if err := os.WriteFile(settingsPath(), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			var expose []string
			if test.expose {
				expose = []string{"API_KEY"}
			}
			request, err := NewWallGrantRequest(false, false, "none", expose)
			if err != nil {
				t.Fatal(err)
			}
			defer request.Close()
			ctx := WithGrantRequest(t.Context(), request)
			values, err := FetchHostSecrets(ctx, host, []string{"API_KEY"}, false)
			if _, markerErr := os.Stat(marker); !errors.Is(markerErr, os.ErrNotExist) {
				t.Fatal("a secret command ran before its required checks and answer")
			}
			if test.code != "" {
				var user *UserError
				if !errors.As(err, &user) || user.Code != test.code || values != nil {
					t.Fatalf("secret refusal = %v, values %v", err, values)
				}
				return
			}
			var question *ConfirmError
			if !errors.As(err, &question) {
				t.Fatalf("shared secret had no fresh terminal answer: %v", err)
			}
			ctx, err = ConfirmGrant(ctx, err)
			if err != nil {
				t.Fatal(err)
			}
			values, err = FetchHostSecrets(ctx, host, []string{"API_KEY"}, false)
			if err != nil || values["API_KEY"] != "generated-session-value" {
				t.Fatalf("approved exposed shared secret = %v, %v", values, err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("the eligible command never ran after its own answer")
			}
		})
	}
}

func sessionMachineRemovalFixtureV1(t *testing.T, states ...string) (*SessionMachineRemovalV1, *sessionMachineLifecycleFixtureV1) {
	t.Helper()
	host := Host{Name: "m-work", Machine: &Machine{Provider: "session-machine-removal-test", ID: "original-machine", Session: "work", SessionID: "abc23def"}}
	prepareSavedFixture(t, host, TierShared, strings.Repeat("7", 32), false)
	lifecycle := &sessionMachineLifecycleFixtureV1{states: states}
	opens := 0
	RegisterProvider(host.Machine.Provider, func(selected Host) (Lifecycle, error) {
		opens++
		if !SameEntry(selected, host) {
			return nil, errors.New("provider opened another saved host")
		}
		return lifecycle, nil
	})
	operation, err := OpenSessionMachineRemovalV1(t.Context(), host.Name)
	if err != nil || operation == nil || opens != 1 {
		t.Fatalf("removal open = %v, %v, provider opens %d", operation, err, opens)
	}
	t.Cleanup(func() {
		if err := operation.Close(); err != nil {
			t.Error(err)
		}
	})
	return operation, lifecycle
}

func confirmSessionMachineEmptyFixtureV1(t *testing.T, operation *SessionMachineRemovalV1, lifecycle *sessionMachineLifecycleFixtureV1) {
	t.Helper()
	bridge := newPrepareBridgeTransport(t, strings.Repeat("7", 32), []string{"session.id"}, func(request proto.Request, _ *proto.Writer) (any, *proto.Error, bool) {
		if request.Op == "ls" {
			lifecycle.record("ls")
			return []Session{}, nil, false
		}
		return nil, &proto.Error{Code: "unknown", Message: request.Op}, false
	})
	conn, err := dial(operation.Context(), operation.Host(), route{t: bridge})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := operation.ConfirmAbsentRoot(operation.Context(), conn); err != nil {
		t.Fatal(err)
	}
}

func TestSessionMachineRemovalUsesOneLifecycleAndConfirmedFinalAbsence(t *testing.T) {
	operation, lifecycle := sessionMachineRemovalFixtureV1(t, MachineRunning, MachineRunning, MachineMissing)
	if _, err := operation.Status(); err != nil {
		t.Fatal(err)
	}
	if err := operation.DestroyAndForget(); err == nil {
		t.Fatal("status metadata replaced the actual original-session deletion")
	}
	confirmSessionMachineEmptyFixtureV1(t, operation, lifecycle)
	if err := operation.DestroyAndForget(); err != nil {
		t.Fatal(err)
	}
	if got, want := lifecycle.snapshot(), []string{"status", "ls", "status", "destroy", "status"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle calls = %v, want %v", got, want)
	}
	if _, err := FindHost(operation.Host().Name); err == nil {
		t.Fatal("confirmed destruction kept the saved machine")
	}
}

func TestSessionMachineRemovalAlreadyGoneSkipsDestroy(t *testing.T) {
	operation, lifecycle := sessionMachineRemovalFixtureV1(t, MachineMissing)
	if state, err := operation.Status(); err != nil || state != MachineMissing {
		t.Fatalf("missing = %v, %v", state, err)
	}
	if err := operation.DestroyAndForget(); err != nil {
		t.Fatal(err)
	}
	if got, want := lifecycle.snapshot(), []string{"status", "status"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("gone machine calls = %v", got)
	}
	if _, err := FindHost(operation.Host().Name); err == nil {
		t.Fatal("already-gone machine was not forgotten")
	}
}

func TestSessionMachineMissingObservationCannotDestroyReappearedResource(t *testing.T) {
	operation, lifecycle := sessionMachineRemovalFixtureV1(t, MachineMissing, MachineRunning)
	if state, err := operation.Status(); err != nil || state != MachineMissing {
		t.Fatalf("first status = %s, %v", state, err)
	}
	if err := operation.DestroyAndForget(); err == nil {
		t.Fatal("an earlier missing status replaced the original session delete")
	}
	if got, want := lifecycle.snapshot(), []string{"status", "status"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reappeared resource reached destroy: %v", got)
	}
	if _, err := FindHost(operation.Host().Name); err != nil {
		t.Fatal("a reappeared resource was forgotten")
	}
}

func TestSessionMachineRemovalKeepsHostUntilSameProviderConfirmsAbsence(t *testing.T) {
	operation, lifecycle := sessionMachineRemovalFixtureV1(t, MachineRunning)
	if _, err := operation.Status(); err != nil {
		t.Fatal(err)
	}
	confirmSessionMachineEmptyFixtureV1(t, operation, lifecycle)
	err := operation.DestroyAndForget()
	var user *UserError
	if !errors.As(err, &user) || user.Code != "destroy_unconfirmed" {
		t.Fatalf("unconfirmed destroy = %v", err)
	}
	if _, err := FindHost(operation.Host().Name); err != nil {
		t.Fatal("an acknowledged Destroy without positive absence forgot the host")
	}
	if got, want := lifecycle.snapshot(), []string{"status", "ls", "status", "destroy", "status"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unconfirmed calls = %v", got)
	}
}

func rotateSessionMachineMembershipFixtureV1(t *testing.T, operation *SessionMachineRemovalV1) {
	t.Helper()
	data, err := os.ReadFile(hostsPath())
	if err != nil {
		t.Fatal(err)
	}
	generation, err := newMembershipGeneration()
	if err != nil {
		t.Fatal(err)
	}
	old := membershipGenerationPrefix + operation.removal.source.generation
	if !strings.Contains(string(data), old) {
		t.Fatalf("missing original canonical generation marker in %q", data)
	}
	data = []byte(strings.Replace(string(data), old, membershipGenerationPrefix+generation, 1))
	// Model an external remove/re-add that preserves the complete host row.
	// Its new membership generation must revoke this operation's old Source.
	if err := os.WriteFile(hostsPath(), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSessionMachineRemovalSourceABAStopsProviderAndForget(t *testing.T) {
	operation, lifecycle := sessionMachineRemovalFixtureV1(t, MachineMissing)
	if _, err := operation.Status(); err != nil {
		t.Fatal(err)
	}
	rotateSessionMachineMembershipFixtureV1(t, operation)
	before := lifecycle.snapshot()
	if err := operation.DestroyAndForget(); err == nil {
		t.Fatal("identical saved-row ABA retained provider deletion authority")
	}
	if !reflect.DeepEqual(before, lifecycle.snapshot()) {
		t.Fatal("a stale Source reached the provider")
	}
	if _, err := FindHost(operation.Host().Name); err != nil {
		t.Fatal("stale Source forgot the replacement row")
	}
}
