package host

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/checkpoint"
)

type bridgeRewindPreparationContextKeyV1 struct{}

// The private preparation is issued only by the actual bridge reader's
// rewind.prepare-v1 dispatch. Its token is selection data, never a Source.
type bridgeRewindPreparationV1 struct {
	bridge             *bridge
	operation          *SessionOperation
	warm               *WarmResumeV1
	stopped            *StoppedSessionV1
	cold               *ColdResumeV1
	coldCapture        *ColdResumeV1 // actual incomplete local producer; never restart accounting
	coldCaptureErr     error
	publication        *runtimePublication
	accounting         *resumePreparationLeaseV1
	lifetime           context.Context
	execution          context.Context
	work               context.Context
	cancel             context.CancelCauseFunc
	cancelExecution    context.CancelCauseFunc
	stopClientWatch    func() bool
	active             atomic.Bool
	phase              atomic.Uint32
	selection          api.RewindArgs
	metadata           api.ResumeArgs
	floor              string
	startDigest        string
	token              string
	prepareRequest     uint64
	commitRequest      uint64
	commitEncoded      []byte
	fresh              api.ResumeArgs
	consumed           bool
	members            []*rewindMemberRootV1
	source             *api.SandboxSnapshot
	snapshot           checkpoint.SnapshotV1
	backend            checkpoint.BackendV1
	storage            *rewindStoppedStorageV1
	disk               checkpoint.PreparedRestoreV1
	journal            *rewindJournalOwnerV1
	metadataTxn        *rewindMetadataTransactionV1
	slotSnapshot       rewindSlotSnapshotV1
	artifactsRefreshed bool
	rolledBack         bool
	keep               int
	retry              rewindRetryCustodyV1
	retryCount         int
	startupDelivery    *rewindStartupDeliveryV1
	readyWarm          *WarmResumeV1
	readyUnexpected    *ColdResumeV1
	result             *api.RewindResult
	closeErr           error
}

type rewindRetryCustodyV1 interface {
	checkV1(context.Context, *SessionOperation) error
	previousColdV1() *ColdResumeV1
	Close() error
}

type rewindSlotSnapshotV1 interface {
	SelectRunningSnapshotV1(context.Context, string) (checkpoint.SnapshotV1, error)
	OpenStoppedBackendV1(context.Context, *SessionOperation, *StoppedSessionV1) (checkpoint.BackendV1, func() error, error)
	Close() error
}

type bridgeRewindServiceV1 struct {
	work         sync.Mutex
	closed       atomic.Bool
	plan         atomic.Pointer[bridgeRewindPreparationV1]
	settled      string
	result       *api.RewindResult
	resultDigest string
}

const (
	rewindPreparingV1 uint32 = iota
	rewindPreparedV1
	rewindStoppingV1
	rewindStoppedV1
	rewindDiskPreparedV1
	rewindExchangedV1
	rewindMetadataCommittedV1
	rewindRestartingV1
	rewindCompletedV1
	rewindRolledBackV1
)

func rewindFailureV1(message string) error {
	return UserError("rewind_source", message, "Keep the original rewind preparation and its native owners; retry through the same verified client connection.")
}

func (b *bridge) rewindServiceV1() *bridgeRewindServiceV1 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx == nil || b.ctx.Err() != nil {
		return nil
	}
	if b.rewindPreparationService == nil {
		b.rewindPreparationService = &bridgeRewindServiceV1{}
	}
	return b.rewindPreparationService
}

func (p *bridgeRewindPreparationV1) preparedResultV1() api.RewindPrepareResultV1 {
	return api.RewindPrepareResultV1{V: 1, Prepared: &api.PreparedRewindV1{V: 1, Token: p.token}, Selection: p.selection, Source: cloneRewindSnapshotV1(p.source), StartDigest: p.startDigest}
}

func cloneRewindSnapshotV1(source *api.SandboxSnapshot) *api.SandboxSnapshot {
	if source == nil {
		return nil
	}
	copy := *source
	copy.Members = append([]string(nil), source.Members...)
	return &copy
}

func (p *bridgeRewindPreparationV1) checkCommittedV1(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1, args api.ResumeArgs) error {
	if p == nil || ctx == nil || p.operation != operation || p.cold != cold || !p.active.Load() || !p.consumed || p.prepareRequest == 0 || p.commitRequest == 0 || len(p.commitEncoded) == 0 || p.bridge == nil || p.execution == nil || p.execution.Err() != nil || args.Prepared != nil {
		return rewindFailureV1("the restart lost its actual closed rewind commit")
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return err
	}
	defer clear(encoded)
	if !equalSecretBytesV1(encoded, p.commitEncoded) {
		return rewindFailureV1("the restart values differ from the original accepted rewind commit")
	}
	return p.checkAccountingOriginV1(ctx, operation, cold)
}

func equalSecretBytesV1(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for i := range left {
		difference |= left[i] ^ right[i]
	}
	return difference == 0
}
