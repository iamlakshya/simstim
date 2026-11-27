package host

import (
	"context"
	"errors"
	"os"
	"sort"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Every destructive session consumer retains the same native OP and the
// opaque physical stop source. A manifest's PID, state and runtime paths do
// not enter this gate as evidence that a process or an auxiliary unit ended.
func (owner *purgeOwnerV1) removeSessions() error {
	if owner.phaseDone("sessions") {
		return nil
	}
	manifests, err := trustedSessionManifests()
	if err != nil {
		return err
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].ID < manifests[j].ID })
	for _, observed := range manifests {
		if observed.SandboxOf != "" {
			continue
		}
		if err := owner.removeSession(observed.ID); err != nil {
			return err
		}
	}
	return owner.completePhase("sessions")
}

func (owner *purgeOwnerV1) removeSession(id string) (returned error) {
	operation, err := BeginSessionOperation(owner.ctx, id, time.Second)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, operation.CloseWithError()) }()
	manifest, err := operation.LoadContext(owner.ctx)
	if err != nil {
		return err
	}
	if manifest.Wall == nil {
		return owner.removeOrdinarySessionV1(operation, manifest)
	}
	stopped, stopErr := StopSessionForOperationV1(owner.ctx, operation)
	if stopped == nil {
		return stopErr
	}
	defer func() { returned = errors.Join(returned, stopped.Close()) }()
	if err := stopped.Check(owner.ctx, operation); err != nil {
		return errors.Join(stopErr, err)
	}
	if !owner.args.Force {
		inspection, err := withPurgeAuxiliaryContextV1(owner.ctx, owner, operation, stopped)
		if err != nil {
			return err
		}
		if err := CheckPurgeUnsavedForOperationV1(inspection, operation, stopped); err != nil {
			var wire *proto.Error
			if errors.As(err, &wire) && wire.Code == "unsaved" && owner.ctx.Err() == nil {
				if checkErr := stopped.Check(owner.ctx, operation); checkErr != nil {
					return errors.Join(err, checkErr)
				}
				if rollbackErr := owner.refuseStoppedUnsavedV1(manifest, wire.Message); rollbackErr != nil {
					return errors.Join(err, rollbackErr)
				}
			}
			return err
		}
		if err := stopped.Check(owner.ctx, operation); err != nil {
			return err
		}
	}
	if err := owner.markSessionDestructiveV1(operation, manifest, stopped); err != nil {
		return err
	}
	// A physical source may survive a lost transport acknowledgement. That
	// source still has to pass its own independent native closure check above.
	if err := DestroyStoppedSessionResourcesV1(context.WithValue(owner.ctx, purgeStoppedCleanupKeyV1{}, owner), operation, stopped); err != nil {
		return errors.Join(stopErr, err)
	}
	if err := stopped.Check(owner.ctx, operation); err != nil {
		return err
	}
	if manifest.Git != nil {
		// Worktree deletion requires its original native creation producer. Old
		// manifests' CreatedBy/path fields alone never authorise recursive removal.
		owner.reportItem(false, "worktree", manifest.Git.Worktree, "the worktree has no current native creation receipt")
	}
	if err := finishStoppedSessionMetadataV1(owner.ctx, owner, operation, stopped); err != nil {
		return err
	}
	directory, err := operation.native.root.Open(".")
	if err != nil {
		return err
	}
	info, err := directory.Stat()
	_ = directory.Close()
	if err != nil {
		return err
	}
	directoryRef := refOf(info)
	owner.reportItem(true, "session", id, "")
	return owner.gate(func(control *placementControl, _ *os.Root) error {
		if control.Sessions == nil {
			control.Sessions = map[string]purgeSessionPlan{}
		}
		control.Sessions[id] = purgeSessionPlan{ID: id, Name: manifest.Name, Directory: directoryRef, Stopped: true, Cleaned: true}
		return nil
	})
}

func (owner *purgeOwnerV1) markSessionDestructiveV1(operation *SessionOperation, manifest *Manifest, stopped *StoppedSessionV1) error {
	if operation == nil || manifest == nil || !operation.Valid(manifest.ID) {
		return sessionLeaseError("the destructive purge phase lost its original operation")
	}
	if err := stopped.Check(owner.ctx, operation); err != nil {
		return err
	}
	file, err := operation.native.root.Open(".")
	if err != nil {
		return err
	}
	info, err := file.Stat()
	_ = file.Close()
	if err != nil {
		return err
	}
	ref := refOf(info)
	return owner.gate(func(control *placementControl, _ *os.Root) error {
		if control.Sessions == nil {
			control.Sessions = map[string]purgeSessionPlan{}
		}
		if prior, ok := control.Sessions[manifest.ID]; ok && !samePurgeDirectoryRef(prior.Directory, ref) {
			return sessionLeaseError("the destructive purge subject changed its original root")
		}
		control.Sessions[manifest.ID] = purgeSessionPlan{ID: manifest.ID, Name: manifest.Name, Directory: ref, Stopped: true}
		control.Destructive = true
		return nil
	})
}

func (owner *purgeOwnerV1) refuseStoppedUnsavedV1(manifest *Manifest, reason string) error {
	owner.report.Unsaved = append(owner.report.Unsaved, api.PurgeSession{ID: manifest.ID, Name: manifest.Name, Reason: reason})
	return owner.gate(func(control *placementControl, _ *os.Root) error {
		if control.Destructive {
			return nil
		}
		// Only a positively inspected unsaved-work refusal can withdraw this
		// exact purge mark. The separate user drain file is never changed here.
		control.Drain, control.Nonce, control.Phase = "", "", ""
		control.Report = nil
		return nil
	})
}
