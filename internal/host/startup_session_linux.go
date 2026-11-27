//go:build linux

package host

import (
	"context"
	"errors"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func startOwnedReservedSession(m *Manifest, secrets map[string]api.Secret, reservation *sessionReservation, operation *SessionOperation) (_ *SessionInfo, failure error) {
	if reservation == nil || operation == nil || m == nil || reservation.id != operation.Primary() || m.ID != operation.Primary() {
		return nil, sessionLeaseError("the new startup has no original reserved operation")
	}
	started, launch, err := startOwnedSessionOperation(operation.ownerContext, operation, secrets, nil, false, initialDelivery(m))
	if launch != nil {
		reservation.launched = launch.started
		m.launchStarted = launch.started
		m.Run.Unit, m.Run.HolderPID = launch.manifest.Run.Unit, launch.manifest.Run.HolderPID
	}
	if err != nil {
		if launch != nil {
			// Close synchronously cancels and native-joins the actual original
			// helper before this function may release its outside parent OP.
			closeErr := launch.Close()
			if launch.cleanupConfirmed && launch.exited {
				if checkErr := operation.native.check(context.Background()); checkErr == nil {
					_ = deleteLocked(m.ID, nil, &DeleteResult{})
				}
			}
			failure = errors.Join(err, closeErr)
			if launch.started && !(launch.cleanupConfirmed && launch.exited) {
				return nil, &keptError{err: UserError("start_failed", "startup failed; the original session "+m.ID+" was retained: "+failure.Error(),
					"Inspect its log and actual native owner with the host doctor before resuming or removing it.")}
			}
		}
		return nil, UserError("start_failed", err.Error(), "Check the session log with the host doctor, then try again.")
	}
	defer func() { failure = errors.Join(failure, launch.Close()) }()
	info := publicationInfoFrom(started, true)
	if err := checkSecretPublicationResponse(info); err != nil {
		return nil, err
	}
	return &info, nil
}

// startOwnedSessionOperation is shared by actual New, Resume and automatic
// Restore producers while their ONE original parent operation remains held.
// It has no raw-manifest boot overload and never starts an ordinary fallback.
func startOwnedSessionOperation(ctx context.Context, operation *SessionOperation, secrets map[string]api.Secret, members []api.MemberResumeArgs, allowMissing bool, delivery *api.SecretDeliveryIntent) (*Manifest, *startupLaunchV1, error) {
	launch, err := launchSupervisorOperation(ctx, operation)
	if err != nil {
		return nil, launch, err
	}
	// Secret delivery and start travel on the exact hello/SCM-loan line.
	transfer, cancel := context.WithTimeout(launch.placement.ctx, 5*time.Second)
	err = launch.deliverStart(transfer, secrets, members, allowMissing, delivery)
	cancel()
	if err != nil {
		return nil, launch, err
	}
	// The actual supervisor owns its existing boot budget and publishes a
	// failure/close when it expires. Do not invent a shorter metadata-derived
	// parent budget before the original child has published its boot record.
	started, err := launch.waitReady(launch.placement.ctx)
	return started, launch, err
}

// startSandboxPrepareSessionV1 launches the fixed test-boot supervisor under
// the caller's 60-second and connection-bound context so timeout or client
// loss interrupts delivery and readiness immediately before joined cleanup.
func startSandboxPrepareSessionV1(ctx context.Context, operation *SessionOperation) (*Manifest, *startupLaunchV1, error) {
	if operation == nil || !operation.IsSandboxPrepareV1() {
		return nil, nil, sessionLeaseError("sandbox prepare start has no original prepare operation")
	}
	launch, err := launchSupervisorOperation(ctx, operation)
	if err != nil {
		return nil, launch, err
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		if launch.line != nil {
			_ = launch.line.SetDeadline(time.Now())
		}
	})
	defer func() {
		if !stop() {
			<-done
		}
	}()
	if deadline, ok := ctx.Deadline(); ok && launch.line != nil {
		_ = launch.line.SetDeadline(deadline)
	}
	transfer, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = launch.deliverStart(transfer, nil, nil, false, nil)
	cancel()
	if err != nil {
		return nil, launch, err
	}
	started, err := launch.waitReady(ctx)
	return started, launch, err
}
