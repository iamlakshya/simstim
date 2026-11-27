package host

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// resumeColdForPreparationV1 is entered only by the actual retained native
// ColdResumeV1 method. It never reacquires OP or infers absence from metadata.
func resumeColdForPreparationV1(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1, a api.ResumeArgs) (*api.ResumeResult, error) {
	origin, err := requireResumePreparationAccountingOriginV1(ctx, operation)
	if err != nil {
		return nil, err
	}
	accounting, err := origin.committedLeaseV1(ctx, operation, cold, a)
	if err != nil {
		return nil, err
	}
	if err := accounting.Check(ctx, operation, cold); err != nil {
		return nil, err
	}
	root, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateRestartWall(root); err != nil {
		return nil, err
	}
	if root.Wall == nil || root.Wall.Driver == "plain" {
		return nil, resumePreparationErrorV1("the native cold preparation has no protected sandbox")
	}
	if err := checkResumeExpectedImageV1(ctx, operation, a.ExpectedImageV1); err != nil {
		return nil, err
	}
	if a.Snapshot != nil || a.Members != nil || a.ID != root.ID {
		var structural error
		if origin.rewind != nil {
			structural = origin.rewind.checkColdRestartCohortV1(ctx, a.Snapshot)
		} else {
			structural = CheckSandboxSnapshotLocked(a.ID, root.ID, a.Snapshot)
		}
		if structural != nil {
			return nil, structural
		}
	}
	if err := ApplyResumeLayers(root, a.Layers); err != nil {
		return nil, err
	}
	if err := ApplyRepositoryResume(root, a); err != nil {
		return nil, err
	}
	children, err := SandboxMembers(root.ID)
	if err != nil {
		return nil, err
	}
	if len(children) > 0 && a.Snapshot == nil {
		return nil, snapshotChanged()
	}
	childValues, err := checkedResumeMembers(root.ID, children, a.Members)
	if err != nil {
		return nil, err
	}
	primaryValues, err := checkedResumeValues(root.Secrets, a.Secrets)
	if err != nil {
		return nil, err
	}
	delivery, err := prepareResumeSecretDelivery(root, a.SecretDelivery, primaryValues)
	if err != nil {
		return nil, err
	}
	memberIntents := make(map[string]*api.SecretDeliveryIntent, len(a.Members))
	for _, member := range a.Members {
		memberIntents[member.ID] = member.SecretDelivery
	}
	members := make([]api.MemberResumeArgs, 0, len(children))
	defer func() { clearProtectedTrustedMembersV1(members) }()
	for _, child := range children {
		if _, err := ResumeSandboxMember(child, root); err != nil {
			return nil, err
		}
		intent, err := prepareResumeSecretDelivery(child, memberIntents[child.ID], childValues[child.ID])
		if err != nil {
			return nil, err
		}
		members = append(members, api.MemberResumeArgs{ID: child.ID, Secrets: childValues[child.ID], SecretDelivery: intent, ProtectedTrustedSecretValues: maps.Clone(originalProtectedTrustedMemberV1(a.Members, child.ID))})
	}
	if _, _, err := resumeArgv(root); err != nil {
		return nil, err
	}
	if err := validateResumeInputs(root, a.Inputs); err != nil {
		return nil, err
	}
	if err := prepareResumeEnvironment(root, a.Environment); err != nil {
		return nil, err
	}
	if err := applyAdmittedRestartMemoryV1(operation, root); err != nil {
		return nil, err
	}
	if err := cold.CheckManifest(ctx, operation, root); err != nil {
		return nil, err
	}
	if root.Policy == nil {
		return nil, namedSSHStartError("the cold sandbox has no canonical selected policy")
	}
	if err := checkNewNamedSSHSelection(*root.Policy, a.NamedSSHKeys); err != nil {
		return nil, err
	}
	if origin.rewind != nil {
		if err := origin.rewind.captureStartupDeliveryV1(ctx, cold, root, a); err != nil {
			return nil, err
		}
	} else {
		if err := captureResumeNamedSSHKeysV1(ctx, operation, cold, root, a); err != nil {
			return nil, err
		}
		if err := captureResumeProtectedTrustedSecretsV1(ctx, operation, cold, root, a); err != nil {
			return nil, err
		}
	}
	root.HostLabel = a.HostLabel
	root.Inputs = CloneInputSummary(a.Inputs)
	root.Run.RespawnCount = 0
	// This adapter carries the existing reservation into the actual restart.
	// It deliberately leaves its charge until the actual native loan takeover.
	if err := accounting.TransferToStarting(ctx, operation, cold, root); err != nil {
		return nil, err
	}
	_, why, err := startAgain(root, again{operation: operation, label: resumedLabel, reason: "sandbox sessions started again", secrets: primaryValues, secretDelivery: delivery, members: members, allowMissing: true})
	if err != nil {
		if refused := restartRefusal(err); refused != nil {
			return nil, refused
		}
		return nil, UserError("start_failed", "the original cold sandbox could not start: "+err.Error(), "Check the supervisor log before preparing another resume.")
	}
	selected, err := LoadManifest(a.ID)
	if err != nil {
		return nil, err
	}
	current, err := LoadManifest(operation.Primary())
	if err != nil {
		return nil, err
	}
	result := &api.ResumeResult{Session: publicationInfoFrom(selected, alive(a.ID), current), Why: why}
	if err := checkSecretPublicationResponse(result); err != nil {
		return nil, err
	}
	return result, nil
}

// The private key carrier can be installed only by the SAME original cold
// producer, after its locked memory reservation and canonical names checks.
func captureResumeNamedSSHKeysV1(ctx context.Context, operation *SessionOperation, cold *ColdResumeV1, m *Manifest, a api.ResumeArgs) error {
	origin, err := requireResumePreparationAccountingOriginV1(ctx, operation)
	if err != nil {
		return err
	}
	accounting, err := origin.committedLeaseV1(ctx, operation, cold, a)
	if err != nil {
		return err
	}
	if m == nil || m.Policy == nil {
		return namedSSHStartError("the named keys lost their original cold resume source")
	}
	if err := errors.Join(accounting.Check(ctx, operation, cold), cold.CheckManifest(ctx, operation, m)); err != nil {
		return err
	}
	keys := a.NamedSSHKeys
	if err := checkNewNamedSSHSelection(*m.Policy, keys); err != nil {
		return err
	}
	if keys == nil {
		return nil
	}
	for _, name := range keys.Names {
		if _, ordinary := a.Secrets[name]; ordinary {
			return namedSSHStartError("an SSH key cannot enter ordinary secret delivery")
		}
		for _, member := range a.Members {
			if _, ordinary := member.Secrets[name]; ordinary {
				return namedSSHStartError("an SSH key cannot enter a member's ordinary secret delivery")
			}
		}
	}
	operation.gate.Lock()
	defer operation.gate.Unlock()
	if !operation.active.Load() || operation.namedSSHKeys != nil || operation.namedSSHKeysConsumed {
		return namedSSHStartError("the original cold resume key delivery was consumed or ended")
	}
	selected := slices.Clone(m.Policy.Sandbox.SSHKeys)
	slices.Sort(selected)
	if !slices.Equal(selected, keys.Names) {
		return namedSSHStartError("the cold named-key selection changed")
	}
	operation.namedSSHKeys = api.CloneNamedSSHKeysV1(keys)
	return nil
}

func originalProtectedTrustedMemberV1(members []api.MemberResumeArgs, id string) map[string]api.Secret {
	for _, member := range members {
		if member.ID == id {
			return member.ProtectedTrustedSecretValues
		}
	}
	return nil
}
