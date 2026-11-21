//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

// NoneColdResumeV1 is a distinct host-account continuation. It retains only
// the original outside OP/root, real reboot epoch and exclusive holder role.
// It owns no VM artefact, declared quantum or whole-unit stopped subject.
type NoneColdResumeV1 struct {
	self                  *NoneColdResumeV1
	life                  sync.Mutex
	mu                    sync.Mutex
	operation             *SessionOperation
	preparation           *bridgeResumePreparationV1
	holder                *sessionFileLease
	manager               *nativeUserManagerV1
	source                Manifest
	boot                  string
	bound, handed, closed bool
	startup               *startupLaunchV1
	creation              *os.File
	metadata              *os.File
	metadataRef           cachefs.Reference
	metadataBytes         []byte
	originalBytes         []byte
	sameBoot, published   bool
	memberIDs             []string
	directoryCleanup      *noneDirectoryCleanupV1
	managerDiagnostic     error
}

func noneColdRoleV1(m *Manifest) bool {
	return NoneSupervisorShapeV1(m)
}

func captureNoneColdResumeV1(ctx context.Context, p *bridgeResumePreparationV1) (partial *NoneColdResumeV1, returned error) {
	if ctx == nil || p == nil || p.bridge == nil || p.operation == nil || !p.active.Load() || p.bridge.ctx == nil || p.bridge.ctx.Err() != nil {
		return nil, resumePreparationErrorV1("the None continuation has no actual original prepare request")
	}
	operation := p.operation
	if operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) {
		return nil, sessionLeaseError("the None continuation has no original owning OP")
	}
	if err := errors.Join(CheckCaller(), CheckStartContext(ctx), ctx.Err()); err != nil {
		return nil, err
	}
	before, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	boot := BootID()
	if !noneColdRoleV1(before) || !auxiliaryBootIDV1(boot) || !auxiliaryBootIDV1(before.Run.BootID) {
		return nil, sessionLeaseError("the original None continuation has no supported canonical role and kernel boot epoch")
	}
	sameBoot := before.Run.BootID == boot
	if sameBoot && !validNoneCleanupV1(before, boot) {
		return nil, sessionLeaseError("the original None run has no trusted complete host-account cleanup for this boot and generation")
	}
	holder, err := openNoneColdHolderV1(ctx, operation, before)
	if err != nil {
		return nil, err
	}
	complete := false
	var manager *nativeUserManagerV1
	var result *NoneColdResumeV1
	defer func() {
		if !complete {
			cleanup := holder.Close()
			var managerDiagnostic error
			if manager != nil {
				diagnostic, unknown := retireNativeManagerV4(context.Background(), manager)
				managerDiagnostic = diagnostic
				returned = errors.Join(returned, diagnostic)
				cleanup = errors.Join(cleanup, unknown)
			}
			if result != nil && result.metadata != nil {
				cleanup = errors.Join(cleanup, result.metadata.Close())
			}
			returned = errors.Join(returned, cleanup)
			if cleanup != nil {
				if result == nil {
					result = &NoneColdResumeV1{operation: operation, preparation: p, holder: holder, manager: manager, source: CloneManifest(*before), boot: boot, sameBoot: sameBoot}
					result.self = result
				}
				result.managerDiagnostic = managerDiagnostic
				partial = result
			}
		}
	}()
	if err := operation.native.rootRef.CheckRoot(holder.root); err != nil {
		return nil, err
	}
	manager, err = openNativeUserManagerV1(ctx)
	if err != nil && (manager != nil || !errors.Is(err, os.ErrNotExist)) {
		return nil, err
	}
	if sameBoot {
		_, managerNamespace, err := CgroupNamespaces()
		if err != nil {
			return nil, err
		}
		if manager != nil || managerNamespace != "" || before.Run.Unit != "" {
			return nil, sessionLeaseError("this same-boot None continuation needs the actual original manager custody class; the managerless issuer cannot replace it")
		}
	}
	result = &NoneColdResumeV1{operation: operation, preparation: p, holder: holder, manager: manager, source: CloneManifest(*before), boot: boot, sameBoot: sameBoot}
	result.self = result
	result.memberIDs, err = captureNoneColdMemberIDsV1(ctx, operation)
	if err != nil {
		return nil, err
	}
	if err := result.captureMetadataV1(ctx, nil); err != nil {
		return nil, err
	}
	result.originalBytes = append([]byte(nil), result.metadataBytes...)
	if err := result.checkVacancyV1(ctx); err != nil {
		return nil, err
	}
	complete = true
	return result, nil
}

func (cold *NoneColdResumeV1) checkOriginV1(ctx context.Context) error {
	if cold == nil || cold.self != cold {
		return resumePreparationErrorV1("the original None continuation is absent")
	}
	p := cold.preparation
	if ctx == nil || p == nil || !p.active.Load() || p.noneCold != cold || p.operation != cold.operation || p.bridge == nil || p.bridge.ctx == nil || p.bridge.ctx.Err() != nil || p.warm != nil || p.cold != nil || len(p.token) != 64 {
		return resumePreparationErrorV1("the None continuation differs from its actual prepare owner")
	}
	p.bridge.mu.Lock()
	service := p.bridge.resumePreparationService
	p.bridge.mu.Unlock()
	if service == nil || service.plan.Load() != p {
		return resumePreparationErrorV1("the original None preparation is no longer registered")
	}
	return ctx.Err()
}

func (cold *NoneColdResumeV1) checkVacancyV1(ctx context.Context) error {
	if cold == nil || cold.self != cold || ctx == nil || cold.closed || cold.operation == nil || cold.operation.native == nil || cold.operation.native.borrowed || !cold.operation.Valid(cold.operation.Primary()) || cold.holder == nil || !auxiliaryBootIDV1(cold.boot) || BootID() != cold.boot {
		return sessionLeaseError("the original None continuation ended or changed")
	}
	if err := errors.Join(CheckCaller(), ctx.Err(), cold.holder.check(ctx), cold.operation.native.rootRef.CheckRoot(cold.holder.root)); err != nil {
		return err
	}
	current, err := cold.operation.LoadContext(ctx)
	if err != nil {
		return err
	}
	if !noneColdRoleV1(current) || current.ID != cold.source.ID {
		return sessionLeaseError("the original None role acquired sandbox or runtime authority")
	}
	if err := cold.checkMetadataV1(ctx); err != nil {
		return err
	}
	if cold.sameBoot {
		if !validNoneCleanupV1(&cold.source, cold.boot) {
			return sessionLeaseError("the original None cleanup no longer matches its boot and run")
		}
		if err := checkNoneColdActorsV1(ctx, &cold.source, cold.memberIDs...); err != nil {
			return err
		}
		_, manager, err := CgroupNamespaces()
		if err != nil {
			return err
		}
		if cold.manager != nil || manager != "" || cold.source.Run.Unit != "" {
			return sessionLeaseError("the current-boot None source lost its actual managerless boundary")
		}
	}
	if cold.manager != nil {
		if err := cold.manager.checkNativeManager(); err != nil {
			return err
		}
		if err := cold.manager.requireAuxiliaryUnitAbsentV1(ctx, UnitName(current.ID)); err != nil {
			return err
		}
		aux, err := AuxUnitNameV1(current.ID)
		if err != nil {
			return err
		}
		if err := cold.manager.requireAuxiliaryUnitAbsentV1(ctx, aux); err != nil {
			return err
		}
	}
	return errors.Join(cold.holder.check(ctx), ctx.Err())
}

func (cold *NoneColdResumeV1) Check(ctx context.Context, operation *SessionOperation) error {
	if cold == nil || cold.self != cold || operation == nil || cold.operation != operation {
		return sessionLeaseError("the original None continuation is absent")
	}
	cold.mu.Lock()
	defer cold.mu.Unlock()
	if err := cold.checkOriginV1(ctx); err != nil {
		return err
	}
	if cold.handed {
		return cold.checkTakeoverV1(ctx)
	}
	return cold.checkVacancyV1(ctx)
}

func (cold *NoneColdResumeV1) CheckManifest(ctx context.Context, operation *SessionOperation, next *Manifest) error {
	if err := cold.Check(ctx, operation); err != nil {
		return err
	}
	if next == nil || next.ID != operation.Primary() || next.path != "" || !noneColdRoleV1(next) || !reflect.DeepEqual(next.Wall, cold.source.Wall) {
		return sessionLeaseError("the None continuation changed its original host-account role")
	}
	return ValidateManifestStart(next)
}

func (cold *NoneColdResumeV1) Close() error {
	if cold == nil {
		return nil
	}
	if cold.self != cold {
		return sessionLeaseError("a copied None continuation cannot release the original native owner")
	}
	cold.life.Lock()
	defer cold.life.Unlock()
	cold.mu.Lock()
	if cold.closed {
		cold.mu.Unlock()
		return nil
	}
	launch := cold.startup
	if launch == nil && cold.bound && cold.operation != nil {
		launch = cold.operation.startup
	}
	cold.mu.Unlock()
	if launch != nil {
		if err := launch.Close(); err != nil {
			return err
		}
	}
	if cold.directoryCleanup != nil {
		if err := cold.directoryCleanup.Close(); err != nil {
			return err
		}
	}
	if err := cold.restoreUnstartedMetadataV1(); err != nil {
		return err
	}
	cold.mu.Lock()
	defer cold.mu.Unlock()
	var err error
	if cold.creation != nil {
		err = errors.Join(err, cold.creation.Close())
	}
	if cold.metadata != nil {
		err = errors.Join(err, cold.metadata.Close())
	}
	if cold.holder != nil {
		err = errors.Join(err, cold.holder.Close())
	}
	if cold.manager != nil {
		err = errors.Join(err, closeNativeManagerResourceV4(cold.manager, cold.operation.Primary(), &cold.managerDiagnostic))
	}
	if err == nil {
		cold.closed = true
	}
	return err
}

func (cold *NoneColdResumeV1) Resume(ctx context.Context, operation *SessionOperation, args api.ResumeArgs) (*api.ResumeResult, error) {
	if err := cold.Check(ctx, operation); err != nil {
		return nil, err
	}
	p := cold.preparation
	if p == nil || !p.consumed || p.requestID == 0 || !args.Prepared.Valid() || args.Prepared.Token != p.token || args.NamedSSHKeys != nil || len(args.ProtectedTrustedSecretValues) != 0 {
		return nil, resumePreparationErrorV1("the None restart has no exact actual commit request")
	}
	for _, member := range args.Members {
		if len(member.ProtectedTrustedSecretValues) != 0 {
			return nil, UserError("secret_intent", "the approved secret delivery intent is unsupported or inconsistent", "Use matching helpers and the original locally approved secret names. Do not fall back to exposure.")
		}
	}
	root, err := operation.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	if !noneColdRoleV1(root) {
		return nil, sessionLeaseError("the original None role changed")
	}
	if err := checkResumeExpectedImageV1(ctx, operation, args.ExpectedImageV1); err != nil {
		return nil, err
	}
	if args.Snapshot != nil || args.Members != nil || args.ID != root.ID {
		if err := CheckSandboxSnapshotLocked(args.ID, root.ID, args.Snapshot); err != nil {
			return nil, err
		}
	}
	if err := ApplyResumeLayers(root, args.Layers); err != nil {
		return nil, err
	}
	if err := ApplyRepositoryResume(root, args); err != nil {
		return nil, err
	}
	if err := validateResumeInputs(root, args.Inputs); err != nil {
		return nil, err
	}
	if args.Inputs != nil || root.Inputs != nil {
		return nil, sessionLeaseError("a None continuation cannot introduce copied sandbox inputs")
	}
	primary, err := checkedResumeValues(root.Secrets, args.Secrets)
	if err != nil {
		return nil, err
	}
	delivery, err := prepareResumeSecretDelivery(root, args.SecretDelivery, primary)
	if err != nil {
		return nil, err
	}
	if delivery != nil {
		if _, _, err := PrepareSecretDelivery(delivery, primary, false, recordedNoneNamesV1(root.Secrets)); err != nil {
			return nil, err
		}
	}
	children, err := SandboxMembers(root.ID)
	if err != nil {
		return nil, err
	}
	if len(children) > 0 && args.Snapshot == nil {
		return nil, snapshotChanged()
	}
	values, err := checkedResumeMembers(root.ID, children, args.Members)
	if err != nil {
		return nil, err
	}
	members := make([]api.MemberResumeArgs, 0, len(children))
	for _, child := range children {
		if !IsNoneSupervisor(child) {
			return nil, sessionLeaseError("a None member changed its original role")
		}
		var selected *api.SecretDeliveryIntent
		for _, member := range args.Members {
			if member.ID == child.ID {
				selected = member.SecretDelivery
			}
		}
		intent, err := prepareResumeSecretDelivery(child, selected, values[child.ID])
		if err != nil {
			return nil, err
		}
		if intent != nil {
			if _, _, err := PrepareSecretDelivery(intent, values[child.ID], false, recordedNoneNamesV1(child.Secrets)); err != nil {
				return nil, err
			}
		}
		members = append(members, api.MemberResumeArgs{ID: child.ID, Secrets: values[child.ID], SecretDelivery: intent})
	}
	if _, _, err := resumeArgv(root); err != nil {
		return nil, err
	}
	if err := prepareResumeEnvironment(root, args.Environment); err != nil {
		return nil, err
	}
	if err := applyAdmittedRestartMemoryV1(operation, root); err != nil {
		return nil, err
	}
	if err := cold.CheckManifest(ctx, operation, root); err != nil {
		return nil, err
	}
	cold.mu.Lock()
	if cold.bound || cold.closed {
		cold.mu.Unlock()
		return nil, sessionLeaseError("the None continuation was already consumed")
	}
	operation.gate.Lock()
	if !operation.active.Load() || operation.noneCold != nil || operation.startup != nil {
		operation.gate.Unlock()
		cold.mu.Unlock()
		return nil, sessionLeaseError("the actual operation already owns another restart")
	}
	operation.noneCold = cold
	cold.bound = true
	operation.gate.Unlock()
	cold.mu.Unlock()
	if err := cold.cleanupPreviousNoneDirectoryV1(ctx); err != nil {
		return nil, err
	}
	root.HostLabel = args.HostLabel
	root.Run.RespawnCount = 0
	_, why, err := startAgain(root, again{operation: operation, label: resumedLabel, reason: "unwalled sessions started again", secrets: primary, secretDelivery: delivery, members: members, allowMissing: true})
	if err != nil {
		return nil, err
	}
	selected, err := LoadManifest(args.ID)
	if err != nil {
		return nil, err
	}
	result := &api.ResumeResult{Session: publicationInfoFrom(selected, alive(args.ID), root), Why: why}
	if err := checkSecretPublicationResponse(result); err != nil {
		return nil, err
	}
	return result, nil
}

func recordedNoneNamesV1(names []string) []string { return append([]string{}, names...) }

func (cold *NoneColdResumeV1) CloseDiagnosticsV4() error {
	if cold == nil {
		return nil
	}
	return errors.Join(cold.managerDiagnostic, cold.directoryCleanup.closeDiagnosticsV1())
}
