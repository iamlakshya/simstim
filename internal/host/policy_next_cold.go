package host

import (
	"bytes"
	"errors"
	"os"
	"reflect"

	"github.com/iamlakshya/simstim/internal/api"
)

const PolicyNextCommitOperationV1 = "policy_next_commit_v1"

// ErrPolicyNextUnappliedV1 means the captured next laptop inputs remain data.
// A missing/future/mismatched capsule is preserved, never normalised or armed.
var ErrPolicyNextUnappliedV1 = errors.New("the captured pending policy inputs are not the actual applied projection")

// CheckPolicyNextAppliedV1 is available only on this actual native startup's
// original committed connection. The pure projection match cannot supply the
// native owner, generation, Need, runtime or current-sender authority.
func (owner *OwnedSupervisorLease) CheckPolicyNextAppliedV1(line *StartupLineV1, current *Manifest) error {
	if owner == nil || line == nil || current == nil {
		return sessionLeaseError("pending policy clearing has no original native owner")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	boot := owner.startup
	if owner.native == nil || !owner.Valid(owner.native.id) || boot == nil || !boot.ready || boot.line != line || owner.pending == nil || owner.pending.line != line || current.ID != owner.native.id || current.SandboxOf != "" || current.path != "" || current.Run.Generation != owner.generation || current.Run.HolderPID != os.Getpid() {
		return sessionLeaseError("pending policy clearing is not this actual native ready continuation")
	}
	sender, err := line.currentSender()
	if err != nil {
		return err
	}
	if err := owner.pending.peer.CheckSender(sender); err != nil {
		return err
	}
	if !bytes.Equal(current.VMNeedV1, boot.currentNeed) || !reflect.DeepEqual(current.RuntimeManifest, boot.runtime) {
		return sessionLeaseError("the original native ready policy source changed")
	}
	if len(boot.policyNext) == 0 || !bytes.Equal(current.PolicyNext, boot.policyNext) || !api.PolicyNextMatchesAppliedV1(current.Layers, boot.policyNext) {
		return ErrPolicyNextUnappliedV1
	}
	return nil
}
