//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/publicationlease"
)

// This fixture retains real native sidecar handles and the exact existing
// control transaction. It tests factory/consumer custody below host admission;
// it grants no outside operation, runtime, VM or execution authority.
func agentsFailedCleanupFixtureV1(t *testing.T) (*InstalledAgentsReceiptV1, *os.File, *os.Root, string) {
	t.Helper()
	base := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		dir := filepath.Join(base, key)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
	controlPath := filepath.Join(base, "control")
	if err := os.Mkdir(controlPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := cachefs.OpenPrivateRoot(controlPath, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	lock, err := cachefs.OpenFile(root, "control.lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	lockInfo, err := lock.Stat()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	dirInfo, err := dir.Stat()
	dir.Close()
	if err != nil {
		t.Fatal(err)
	}
	p := &placement{scope: strings.Repeat("a", 64), hostID: strings.Repeat("b", 32), control: controlPath, controlRef: dirInfo, controlLockRef: lockInfo}
	birth, err := placementProcessBirth(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("c", 32)
	op := &placementOperation{owner: p, token: token, record: placementOperationRecord{PID: os.Getpid(), Birth: birth, Kind: "publish", At: time.Now().UTC()}}
	control := placementControl{ScopeFormat: 2, PersistentRoots: currentPersistentRoots(), Version: 1, Scope: p.scope, UID: os.Getuid(), HostID: p.hostID, Control: refOf(dirInfo), ControlLock: refOf(lockInfo), Active: map[string]placementOperationRecord{token: op.record}}
	if err := writePlacementControl(root, control); err != nil {
		t.Fatal(err)
	}
	op.fence, err = publicationlease.CreateReservedV1(t.Context(), root, p.domain(), token, func(record string) error {
		op.record.Fence, op.record.FenceCreating = record, true
		control.Active[token] = op.record
		return writePlacementControl(root, control)
	})
	if err != nil {
		t.Fatal(err)
	}
	op.record.FenceCreating = false
	control.Active[token] = op.record
	if err := writePlacementControl(root, control); err != nil {
		t.Fatal(err)
	}
	loan, err := op.fence.RetainForHelperV1(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	publication := &runtimePublication{ctx: ctx, cancel: cancel, owner: p, operation: op, stop: make(chan struct{}), watched: make(chan struct{})}
	close(publication.watched) // This fixture has no watch goroutine to join.
	receipt := &InstalledAgentsReceiptV1{publication: publication, done: make(chan struct{})}
	t.Cleanup(func() { loan.Close(); _ = receipt.Close() })
	return receipt, loan, root, token
}

func TestAgentsFactoryErrorRetainsExactCleanupSourceInConnectionV1(t *testing.T) {
	actual, loan, root, token := agentsFailedCleanupFixtureV1(t)
	receipt := actual
	failure := errors.New("original agents preparation failed")
	returned := error(failure)
	finishAgentsFactoryV1(&receipt, &returned)
	if receipt != actual || !errors.Is(returned, failure) || !errors.Is(returned, publicationlease.ErrBusy) {
		t.Fatal("factory erased its actual failed cleanup owner", returned)
	}
	service := &agentsDiskService{}
	service.retainAgentsReceiptV1(receipt)
	if err := service.close(); !errors.Is(err, publicationlease.ErrBusy) {
		t.Fatal("connection released a live original helper fence", err)
	}
	if _, ok := service.receipts[actual]; !ok {
		t.Fatal("failed connection Close discarded the original receipt")
	}
	control, found, err := readPlacementControl(root, actual.publication.owner.scope)
	if err != nil || !found || control.Active[token].Fence == "" {
		t.Fatal("failed cleanup erased its active publication", err)
	}
	loan.Close()
	if err := service.close(); err != nil {
		t.Fatal(err)
	}
	if len(service.receipts) != 0 {
		t.Fatal("successful retry retained completed sources")
	}
	control, found, err = readPlacementControl(root, actual.publication.owner.scope)
	if err != nil || !found || len(control.Active) != 0 {
		t.Fatal("native cleanup retry did not retire the exact operation", err)
	}
}

func TestAgentsNextRequestRetriesTheSameFailedFactoryBeforeNewWorkV1(t *testing.T) {
	actual, loan, _, _ := agentsFailedCleanupFixtureV1(t)
	receipt, returned := actual, errors.New("preparation stopped")
	finishAgentsFactoryV1(&receipt, &returned)
	service := &agentsDiskService{}
	service.retainAgentsReceiptV1(receipt)
	if err := service.retryAgentsCleanupV1(); !errors.Is(err, publicationlease.ErrBusy) {
		t.Fatal("new work bypassed the original cleanup", err)
	}
	loan.Close()
	if err := service.retryAgentsCleanupV1(); err != nil {
		t.Fatal(err)
	}
	if service.closed || len(service.receipts) != 0 {
		t.Fatal("retry lost its live connection or original cleanup state")
	}
}

func TestAgentsFactoryClearsReceiptOnlyAfterActualSuccessfulCleanupV1(t *testing.T) {
	actual, loan, _, _ := agentsFailedCleanupFixtureV1(t)
	loan.Close()
	receipt := actual
	failure := errors.New("preparation failed before inventory")
	returned := error(failure)
	finishAgentsFactoryV1(&receipt, &returned)
	if receipt != nil || !errors.Is(returned, failure) || errors.Is(returned, publicationlease.ErrBusy) {
		t.Fatal("successful native cleanup changed the factory error or retained a finished source", returned)
	}
	select {
	case <-actual.done:
	default:
		t.Fatal("successful cleanup did not settle the original receipt")
	}
}
