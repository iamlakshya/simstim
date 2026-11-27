package host

import (
	"context"
	"errors"
	"reflect"

	"github.com/iamlakshya/simstim/internal/version"
)

// This receipt authorises only one legacy record's execution-path transition.
// It supplies no stopped sandbox, disk, secret or cold-resume authority.
type legacyNoneMigrationV1 struct {
	operation         *SessionOperation
	before            Manifest
	boot              string
	stopped           *StoppedOrdinarySessionV1
	holder            *sessionFileLease
	published, closed bool
}

func (migration *legacyNoneMigrationV1) check(ctx context.Context, operation *SessionOperation, before, next *Manifest) error {
	if migration == nil || ctx == nil || operation == nil || operation != migration.operation || migration.closed || migration.published || operation.native == nil || operation.native.borrowed || !operation.Valid(operation.Primary()) || before == nil || next == nil || before.ID != operation.Primary() || next.ID != before.ID || before.Wall != nil || !IsNoneSupervisor(next) || next.Files != nil || next.RuntimeManifest != nil || len(next.VMNeedV1) != 0 || before.SandboxOf != "" || next.SandboxOf != "" || before.Run.BootID != migration.before.Run.BootID || !reflect.DeepEqual(before.Agent, migration.before.Agent) || before.Cwd != migration.before.Cwd || !reflect.DeepEqual(before.Git, migration.before.Git) {
		return sessionLeaseError("the legacy execution-path transition lost its original source")
	}
	if BootID() != migration.boot {
		return sessionLeaseError("the native boot epoch changed during legacy migration")
	}
	if migration.stopped != nil {
		return migration.stopped.Check(ctx, operation)
	}
	if migration.holder == nil || before.Run.BootID == migration.boot || !validNoneMigrationBootV1(before.Run.BootID) || !validNoneMigrationBootV1(migration.boot) {
		return sessionLeaseError("the legacy record has no actual reboot or joined original holder")
	}
	return errors.Join(ctx.Err(), migration.holder.check(ctx), operation.native.rootRef.CheckRoot(migration.holder.root))
}

func (migration *legacyNoneMigrationV1) close() error {
	if migration == nil || migration.closed {
		return nil
	}
	var err error
	if migration.stopped != nil {
		err = errors.Join(err, migration.stopped.Close())
	}
	if migration.holder != nil {
		err = errors.Join(err, migration.holder.Close())
	}
	if err == nil {
		migration.closed = true
	}
	return err
}

func prepareLegacyNoneTransitionV1(operation *SessionOperation, m *Manifest) error {
	if m.Wall != nil {
		return nil
	}
	if operation == nil {
		return sessionLeaseError("a legacy restart needs its original owning operation")
	}
	if operation.noneMigration == nil {
		if err := captureLegacyNoneRebootV1(operation.ownerContext, operation); err != nil {
			return err
		}
	}
	m.Wall = &WallRecord{Level: "none", Driver: "none", Version: version.Version, Reason: "runs as the host account without a wall", At: nowUTCForNoneV1()}
	return nil
}

func finishLegacyNoneTransitionV1(operation *SessionOperation) error {
	if operation == nil || operation.noneMigration == nil {
		return nil
	}
	if !operation.noneMigration.published {
		return sessionLeaseError("the legacy path transition was not durably published")
	}
	return operation.noneMigration.close()
}
