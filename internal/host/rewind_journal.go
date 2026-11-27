package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/checkpoint"
	"github.com/iamlakshya/simstim/internal/proto"
)

const rewindJournalLeafV1 = "rewind-pending-v1.json"

// Every field is recovery DATA. Reading this file cannot construct an OP,
// stopped owner, snapshot, prepared disk or permission to restart a session.
type rewindJournalDataV1 struct {
	Version    int                      `json:"v"`
	Generation string                   `json:"generation"`
	Primary    string                   `json:"primary"`
	Selected   string                   `json:"selected_session"`
	Phase      string                   `json:"phase"`
	Disk       checkpoint.TransactionV1 `json:"disk"`
	Members    []rewindManifestRoleV1   `json:"members"`
	CreatedAt  time.Time                `json:"created_at"`
	UpdatedAt  time.Time                `json:"updated_at"`
}

type rewindManifestRoleV1 struct {
	Session         string            `json:"session"`
	Root            cachefs.Reference `json:"root"`
	Before          cachefs.Reference `json:"before"`
	SavedBeforeName string            `json:"saved_before_name"`
	SavedBefore     cachefs.Reference `json:"saved_before"`
	AfterName       string            `json:"after_name"`
	After           cachefs.Reference `json:"after"`
	Conversation    string            `json:"conversation"`
}

type rewindJournalOwnerV1 struct {
	preparation    *bridgeRewindPreparationV1
	file           *os.File
	ref            cachefs.Reference
	data           rewindJournalDataV1
	removed        bool
	removeSynced   bool
	fileClosed     bool
	pendingCreated os.FileInfo
	pendingName    string
	closeErr       error
}

func newRewindJournalV1(ctx context.Context, p *bridgeRewindPreparationV1, generation string) (*rewindJournalOwnerV1, error) {
	if p == nil || p.operation == nil || p.stopped == nil || p.disk == nil || p.metadataTxn == nil {
		return nil, rewindFailureV1("the journal has no original stopped transaction owners")
	}
	if err := p.stopped.Check(ctx, p.operation); err != nil {
		return nil, err
	}
	if _, err := p.operation.native.root.Lstat(rewindJournalLeafV1); !errors.Is(err, os.ErrNotExist) {
		return nil, errors.Join(UserError("rewind_pending", "this sandbox already has a pending rewind record", "Retain its original native owner and finish that transaction before starting another."), err)
	}
	at := time.Now().UTC()
	j := &rewindJournalOwnerV1{preparation: p, data: rewindJournalDataV1{Version: 1, Generation: generation, Primary: p.operation.Primary(), Selected: p.selection.ID, Phase: "prepared", Disk: p.disk.Journal(), Members: p.metadataTxn.rolesV1(), CreatedAt: at, UpdatedAt: at}}
	if err := j.writeV1(ctx); err != nil {
		return j, err
	}
	return j, nil
}

func (j *rewindJournalOwnerV1) checkOwnerV1(ctx context.Context) error {
	if j == nil || j.preparation == nil || j.preparation.operation == nil || !j.preparation.active.Load() || !j.preparation.operation.Valid(j.preparation.operation.Primary()) {
		return rewindFailureV1("the pending journal lost its actual transaction owner")
	}
	if ctx == nil || ctx.Err() != nil {
		return context.Canceled
	}
	if j.closeErr != nil {
		return j.closeErr
	}
	return j.preparation.operation.native.check(ctx)
}

func (j *rewindJournalOwnerV1) checkV1(ctx context.Context) error {
	if err := j.checkOwnerV1(ctx); err != nil {
		return err
	}
	if j.removed {
		return rewindFailureV1("the original journal has already been unlinked")
	}
	if j.pendingCreated != nil {
		if err := j.reconcileOwnIntentV1(ctx); err != nil {
			return err
		}
	}
	if j.file != nil {
		return j.ref.CheckFile(j.preparation.operation.native.root, rewindJournalLeafV1, j.file, 0600)
	}
	return nil
}

// Reconciliation uses only the native creation receipt captured by this
// actual writer before publication. It never reads journal data as Source.
func (j *rewindJournalOwnerV1) reconcileOwnIntentV1(ctx context.Context) error {
	if j.pendingCreated == nil {
		return nil
	}
	root := j.preparation.operation.native.root
	current, err := cachefs.OpenFile(root, rewindJournalLeafV1, os.O_RDONLY, 0600)
	if err == nil {
		info, statErr := current.Stat()
		if statErr == nil && os.SameFile(j.pendingCreated, info) && j.pendingCreated.Size() == info.Size() && j.pendingCreated.ModTime().Equal(info.ModTime()) {
			ref, captureErr := cachefs.CaptureFile(root, rewindJournalLeafV1, current, 0600)
			if captureErr != nil {
				return errors.Join(captureErr, current.Close())
			}
			if j.file != nil {
				j.closeErr = j.file.Close()
			}
			j.file, j.ref = current, ref
			j.pendingCreated = nil
			j.pendingName = ""
			return errors.Join(j.closeErr, syncCapturedDirectory(root), ctx.Err())
		}
		_ = current.Close()
	}
	if j.file != nil {
		if check := j.ref.CheckFile(root, rewindJournalLeafV1, j.file, 0600); check != nil {
			return errors.Join(err, check)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(rewindFailureV1("the journal has an unknown publication after its writer failed"), err)
	}
	if j.pendingName != "" {
		part, openErr := cachefs.OpenFile(root, j.pendingName, os.O_RDONLY, 0600)
		if openErr == nil {
			info, statErr := part.Stat()
			if statErr != nil || !os.SameFile(j.pendingCreated, info) {
				return errors.Join(rewindFailureV1("the original pending journal part was replaced"), statErr, part.Close())
			}
			if removeErr := root.Remove(j.pendingName); removeErr != nil {
				return errors.Join(removeErr, part.Close())
			}
			if closeErr := part.Close(); closeErr != nil {
				j.closeErr = closeErr
				return closeErr
			}
		} else if !errors.Is(openErr, os.ErrNotExist) {
			return openErr
		}
	}
	j.pendingCreated = nil
	j.pendingName = ""
	return errors.Join(syncCapturedDirectory(root), ctx.Err())
}

func (j *rewindJournalOwnerV1) writeV1(ctx context.Context) (returned error) {
	if err := j.checkV1(ctx); err != nil {
		return err
	}
	p := j.preparation
	if j.data.Version != 1 || j.data.Generation == "" || j.data.Primary != p.operation.Primary() || j.data.Selected != p.selection.ID {
		return rewindFailureV1("the original journal roles changed")
	}
	j.data.UpdatedAt = time.Now().UTC()
	encoded, err := json.Marshal(j.data)
	if err != nil || len(encoded) > proto.MaxFrame {
		return errors.Join(rewindFailureV1("the pending journal exceeds its metadata bound"), err)
	}
	var before os.FileInfo
	if j.file != nil {
		before, err = j.file.Stat()
		if err != nil {
			return err
		}
	}
	created, err := writeOwnedMetadataIntent(p.operation.native.root, rewindJournalLeafV1, encoded, before, func(info os.FileInfo, name string) error { j.pendingCreated = info; j.pendingName = name; return nil })
	if err != nil {
		return err
	}
	file, err := cachefs.OpenFile(p.operation.native.root, rewindJournalLeafV1, os.O_RDONLY, 0600)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil || created == nil || !os.SameFile(created, opened) {
		return errors.Join(rewindFailureV1("the original journal creator changed before its read handle was retained"), err, file.Close())
	}
	ref, err := cachefs.CaptureFile(p.operation.native.root, rewindJournalLeafV1, file, 0600)
	if err != nil {
		return errors.Join(err, file.Close())
	}
	if j.file != nil {
		returned = j.file.Close()
		if returned != nil {
			j.closeErr = errors.Join(j.closeErr, returned)
		}
	}
	j.file, j.ref = file, ref
	j.pendingCreated = nil
	j.pendingName = ""
	return errors.Join(returned, p.operation.native.check(ctx))
}

func (j *rewindJournalOwnerV1) phaseV1(ctx context.Context, phase string) error {
	if j == nil {
		return rewindFailureV1("the pending journal owner is absent")
	}
	j.data.Phase = phase
	if j.preparation.disk != nil {
		j.data.Disk = j.preparation.disk.Journal()
	}
	if j.preparation.metadataTxn != nil {
		j.data.Members = j.preparation.metadataTxn.rolesV1()
	}
	return j.writeV1(ctx)
}

func (j *rewindJournalOwnerV1) removeV1(ctx context.Context) error {
	if j == nil || j.removalDoneV1() {
		return nil
	}
	if j.data.Phase != "completed" && j.data.Phase != "rolled_back" {
		return rewindFailureV1("the journal cannot close an unsettled rewind")
	}
	if err := j.checkOwnerV1(ctx); err != nil {
		return err
	}
	root := j.preparation.operation.native.root
	if !j.removed {
		if err := j.checkV1(ctx); err != nil {
			return err
		}
		if err := root.Remove(rewindJournalLeafV1); err != nil {
			return err
		}
		j.removed = true // Physical unlink; later durability may still fail.
	}
	if !j.removeSynced {
		if err := syncCapturedDirectory(root); err != nil {
			return err
		}
		j.removeSynced = true
	}
	if !j.fileClosed {
		if j.file != nil {
			if err := j.file.Close(); err != nil {
				j.closeErr = err
				return err
			}
		}
		j.fileClosed = true
		j.file = nil
	}
	return j.checkOwnerV1(ctx)
}

// Physical unlink alone cannot settle journal cleanup. The original writer
// retains its OP until both the directory sync and its actual file close end.
func (j *rewindJournalOwnerV1) removalDoneV1() bool {
	return j == nil || j.removed && j.removeSynced && j.fileClosed && j.closeErr == nil
}

// checkRewindJournalForStartV1 is a refusal guard at the real startup owner.
// An existing journal never grants a cold Source. Only its same private
// original commit, actual disk creator and native Cold may continue it.
func checkRewindJournalForStartV1(ctx context.Context, operation *SessionOperation) error {
	if operation == nil || operation.native == nil {
		return sessionLeaseError("startup has no original operation")
	}
	if _, err := operation.native.root.Lstat(rewindJournalLeafV1); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	p := operation.rewindPreparation
	if p == nil || p.operation != operation || !p.active.Load() || !p.consumed || p.journal == nil || p.cold == nil || operation.nativeColdResume != p.cold || p.phase.Load() != rewindRestartingV1 || p.journal.data.Phase != "ready_to_restart" {
		return UserError("rewind_pending", "this sandbox has an unfinished rewind", "Keep its pending files and use the original native rewind owner to finish or roll back.")
	}
	return errors.Join(p.journal.checkV1(ctx), p.cold.Check(ctx, operation))
}
