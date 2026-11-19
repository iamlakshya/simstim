package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
)

// This is an explicit lifecycle fixture, never a native storage issuer/proof.
type diskLifecycleFixture struct {
	disk           *diskResourcesV1
	killed, waited atomic.Int32
}

func (f *diskLifecycleFixture) Start(context.Context) (net.Conn, error) {
	return nil, errors.New("fixture never starts a process")
}
func (f *diskLifecycleFixture) Kill() error                            { f.killed.Add(1); return nil }
func (f *diskLifecycleFixture) Wait() error                            { f.waited.Add(1); return nil }
func (f *diskLifecycleFixture) ownedDiskResourcesV1() *diskResourcesV1 { return f.disk }
func diskLifecycleRoot(t *testing.T) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
func TestDiskWatcherJoinsBeforeRuntimeCleanupAndBorrowedRootsClose(t *testing.T) {
	volume := diskLifecycleRoot(t)
	runtime := &diskLifecycleFixture{disk: &diskResourcesV1{volume: volume}}
	ctx, cancel := context.WithCancel(context.Background())
	seen, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }); cancel() })
	go func() { defer close(done); <-ctx.Done(); close(seen); <-release }()
	s := &Supervisor{m: &host.Manifest{}, sb: runtime, sandboxStartAttempted: true, diskWatcher: &diskWatcherV1{resources: runtime.disk, cancel: cancel, done: done}}
	result := make(chan error, 1)
	go func() { result <- s.closeSandbox() }()
	select {
	case <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher was not cancelled")
	}
	runtime.disk.mu.Lock()
	closed := runtime.disk.closed
	runtime.disk.mu.Unlock()
	if closed || runtime.killed.Load() != 0 || runtime.waited.Load() != 0 {
		t.Fatal("runtime/root cleanup passed an unjoined watcher")
	}
	once.Do(func() { close(release) })
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("owned cleanup did not finish after watcher joined")
	}
	if runtime.killed.Load() != 1 || runtime.waited.Load() != 1 || !runtime.disk.closed {
		t.Fatal("runtime cleanup or borrowed root release was lost")
	}
}
func TestUnstartedFactoryReleaseDoesNotKillWaitOrDestroySavedData(t *testing.T) {
	volume := diskLifecycleRoot(t)
	runtime := &diskLifecycleFixture{disk: &diskResourcesV1{volume: volume}}
	releases := 0
	runtime.disk.release = func() error { releases++; return nil }
	s := &Supervisor{m: &host.Manifest{}, sb: runtime}
	if err := s.closeSandbox(); err != nil {
		t.Fatal(err)
	}
	if runtime.killed.Load() != 0 || runtime.waited.Load() != 0 || releases != 1 || !runtime.disk.closed {
		t.Fatal("unstarted factory used destructive/second runtime cleanup")
	}
}
func TestAuxiliaryDiskCleanupPreservesActualAgentRunAndNamespace(t *testing.T) {
	volume := diskLifecycleRoot(t)
	runtime := &diskLifecycleFixture{disk: &diskResourcesV1{volume: volume}}
	actual := &host.Manifest{ID: "parent234", Armed: true, Run: host.Runtime{Generation: "actual-agent-run"}, Life: &host.Life{Phase: host.LifeStopped}, SecretState: &api.SessionSecretState{Version: 1, Generation: "actual-namespace", Primary: map[string]api.PublicSecretState{"KEY": {Mode: api.SecretModeBound, Armed: true}}}}
	before, _ := json.Marshal(actual)
	owner, cancel := context.WithCancelCause(context.Background())
	s := &Supervisor{m: actual, sb: runtime, lifetime: owner, generation: "private-auxiliary", bootContextGeneration: "private-auxiliary", bootCancel: cancel}
	old := saveManifest
	t.Cleanup(func() { saveManifest = old })
	writes := 0
	saveManifest = func(*host.Manifest) error { writes++; return errors.New("auxiliary operation must not publish") }
	cancel(context.Canceled)
	if err := s.closeSandbox(); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(actual)
	if string(before) != string(after) || writes != 0 {
		t.Fatal("auxiliary cleanup replaced actual member publication context")
	}
}

func TestInitialDiskCheckGivesOwnerCancellationPrecedence(t *testing.T) {
	volume := diskLifecycleRoot(t)
	runtime := &diskLifecycleFixture{disk: &diskResourcesV1{volume: volume}}
	owner, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Supervisor{m: &host.Manifest{Wall: &host.WallRecord{Driver: "process"}}, lifetime: owner, generation: "current", quit: make(chan string, 1), sb: runtime}
	if err := s.prepareDiskWatcherV1(runtime, s.lifetimeContext()); !errors.Is(err, context.Canceled) {
		t.Fatal("initial check lost owner cancellation", err)
	}
	if s.diskStopReason() != nil || s.captureFailed || s.diskAlerted || len(s.quit) != 0 {
		t.Fatal("normal cancellation became a disk failure/alert")
	}
	if err := s.stopDiskWatcherV1(); err != nil {
		t.Fatal(err)
	}
	if err := s.closeDiskResourcesV1(); err != nil {
		t.Fatal(err)
	}
}
func TestLiveOwnerInitialDiskMeasurementFailureRequestsOwnedStop(t *testing.T) {
	volume := diskLifecycleRoot(t)
	if err := volume.Close(); err != nil {
		t.Fatal(err)
	}
	runtime := &diskLifecycleFixture{disk: &diskResourcesV1{volume: volume}}
	s := &Supervisor{m: &host.Manifest{Wall: &host.WallRecord{Driver: "process"}}, lifetime: context.Background(), generation: "current", quit: make(chan string, 1), sb: runtime, rec: record{phase: host.LifeBooting}}
	err := s.prepareDiskWatcherV1(runtime, s.lifetimeContext())
	if err == nil || s.diskStopReason() == nil || !s.captureFailed || !s.stopIntent || len(s.quit) != 1 {
		t.Fatal("real initial measurement failure did not stop the owned run", err)
	}
	_ = s.stopDiskWatcherV1()
	_ = s.closeDiskResourcesV1()
}

func TestInitialDiskBudgetCancellationIsNotAStorageFailure(t *testing.T) {
	volume := diskLifecycleRoot(t)
	runtime := &diskLifecycleFixture{disk: &diskResourcesV1{volume: volume}}
	initial, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Supervisor{m: &host.Manifest{Wall: &host.WallRecord{Driver: "process"}}, lifetime: context.Background(), generation: "current", quit: make(chan string, 1), sb: runtime}
	if err := s.prepareDiskWatcherV1(runtime, initial); !errors.Is(err, context.Canceled) {
		t.Fatal("initial budget cancellation was lost", err)
	}
	if s.diskStopReason() != nil || s.captureFailed || len(s.quit) != 0 {
		t.Fatal("normal boot budget cancellation became disk protection")
	}
	if err := s.stopDiskWatcherV1(); err != nil {
		t.Fatal(err)
	}
	if err := s.closeDiskResourcesV1(); err != nil {
		t.Fatal(err)
	}
}
