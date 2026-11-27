package host

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
)

const ordinaryWorkInspectionUnavailableV1 = "the ordinary worktree has no supported original native work inspection"

// This branch consumes the actual ordinary native source. It neither casts
// that bounded source to a walled stop nor grants VM, slot or guest cleanup.
func (owner *purgeOwnerV1) removeOrdinarySessionV1(operation *SessionOperation, manifest *Manifest) (returned error) {
	if owner == nil || operation == nil || manifest == nil || manifest.Wall != nil || manifest.SandboxOf != "" || !operation.Valid(manifest.ID) {
		return sessionLeaseError("ordinary removal has no original owning operation")
	}
	if manifest.Git != nil && !owner.args.Force {
		// A known unavailable inspection must preserve the live native holder.
		// This is a refusal before Stop, never a clean-work assertion.
		return owner.refuseOrdinaryInspectionBeforeStopV1(operation, manifest)
	}
	stopped, stopErr := StopOrdinarySessionForOperationV1(owner.ctx, operation)
	if stopped == nil {
		return errors.Join(stopErr, sessionLeaseError("the ordinary stop supplied no original native owner"))
	}
	defer func() { returned = errors.Join(returned, stopped.Close()) }()
	if err := stopped.Check(owner.ctx, operation); err != nil {
		return errors.Join(stopErr, err)
	}
	if scope := stopped.ordinaryScopeV1(); scope == "original holder, main process and terminal (N5)" {
		owner.reportItem(false, "processes", manifest.ID, "the ordinary stop joined "+scope+"; escaped same-user programs may remain")
	}
	directory, err := operation.native.root.Open(".")
	if err != nil {
		return err
	}
	info, err := directory.Stat()
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return errors.Join(err, closeErr)
	}
	ref := refOf(info)
	if err := owner.gate(func(control *placementControl, _ *os.Root) error {
		if err := stopped.Check(owner.ctx, operation); err != nil {
			return err
		}
		if control.Sessions == nil {
			control.Sessions = map[string]purgeSessionPlan{}
		}
		if prior, exists := control.Sessions[manifest.ID]; exists && !samePurgeDirectoryRef(prior.Directory, ref) {
			return sessionLeaseError("the ordinary destructive subject changed its original native root")
		}
		plan := control.Sessions[manifest.ID]
		plan.ID, plan.Name, plan.Directory, plan.Stopped = manifest.ID, manifest.Name, ref, true
		control.Sessions[manifest.ID] = plan
		control.Destructive = true
		return nil
	}); err != nil {
		return err
	}
	if err := owner.destroyOrdinarySessionResourcesV1(operation, stopped); err != nil {
		return errors.Join(stopErr, err)
	}
	if manifest.Git != nil {
		if err := owner.removeOrdinaryWorktreeV1(operation, stopped, manifest); err != nil {
			return err
		}
	}
	if err := owner.finishOrdinarySessionMetadataV1(operation, stopped); err != nil {
		return err
	}
	owner.reportItem(true, "session", manifest.ID, "")
	return owner.gate(func(control *placementControl, _ *os.Root) error {
		plan := control.Sessions[manifest.ID]
		plan.ID, plan.Name, plan.Directory, plan.Stopped, plan.Cleaned = manifest.ID, manifest.Name, ref, true, true
		control.Sessions[manifest.ID] = plan
		return nil
	})
}

func (owner *purgeOwnerV1) refuseOrdinaryInspectionBeforeStopV1(operation *SessionOperation, manifest *Manifest) error {
	if owner == nil || owner.ctx == nil || operation == nil || manifest == nil || manifest.Wall != nil || manifest.Git == nil || owner.args.Force || !operation.Valid(manifest.ID) {
		return sessionLeaseError("the ordinary inspection refusal has no original pre-stop operation")
	}
	row := api.PurgeSession{ID: manifest.ID, Name: manifest.Name, Reason: ordinaryWorkInspectionUnavailableV1}
	owner.report.Unsaved = append(owner.report.Unsaved, row)
	if err := owner.gate(func(control *placementControl, _ *os.Root) error {
		if control.Destructive {
			// A partly destructive retry must retain its original purge mark.
			return nil
		}
		// gate checked this SAME native purge owner, nonce and placement.
		// No ordinary Stop ran. The separate user drain remains untouched.
		control.Drain, control.Nonce, control.Phase = "", "", ""
		control.Report = nil
		return nil
	}); err != nil {
		return err
	}
	wire := UserError("unsaved", ordinaryWorkInspectionUnavailableV1, "Keep the host saved and save the worktree, or use --force after reviewing what will be lost.")
	wire.Data, _ = json.Marshal([]api.PurgeSession{row})
	return wire
}
