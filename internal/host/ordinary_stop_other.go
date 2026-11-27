//go:build !linux

package host

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"syscall"

	"github.com/iamlakshya/simstim/internal/proto"
)

type ownedOrdinaryHolderV1 struct{}

func bindOrdinaryHolderSocketsV1(*ownedOrdinaryHolderV1, *Holder) error { return nil }
func (*Holder) closeOriginalOrdinarySocketsV1() error                   { return nil }
func acquireOrdinaryHolderV1(id string) (*ownedOrdinaryHolderV1, func(), error) {
	release, err := acquireHolderLock(id)
	return nil, release, err
}
func prepareHolderMainV1(cmd *exec.Cmd) *holderMainV1 { return &holderMainV1{cmd: cmd, pidfd: -1} }
func signalHolderMainV1(main *holderMainV1, signal syscall.Signal) error {
	if err := main.cmd.Process.Signal(signal); err != nil {
		return fmt.Errorf("signal holder: %w", err)
	}
	if signal == syscall.SIGKILL || signal == syscall.SIGHUP {
		// Non-Linux hosts have no pidfd or child subreaper; the agent leads its
		// PTY session (Setsid), so signalling its process group reaches
		// children that ignore SIGHUP before Wait reaps the leader.
		_ = syscall.Kill(-main.cmd.Process.Pid, signal)
	}
	return nil
}
func readHolderFirstFrameV1(connection net.Conn) (proto.Frame, io.Reader, *StartupLineV1, error) {
	frame, err := proto.ReadFrom(connection)
	return frame, connection, nil, err
}
func (*Holder) serveOrdinaryStopV1(*hclient, *StartupLineV1, bool)  {}
func (*Holder) serveOrdinaryPurgeV2(*hclient, *StartupLineV1, bool) {}

// StoppedOrdinarySessionV1 has no native proof producer on this platform.
type StoppedOrdinarySessionV1 struct{}

func StopOrdinarySessionForOperationV1(context.Context, *SessionOperation) (*StoppedOrdinarySessionV1, error) {
	return nil, sessionLeaseError("an ordinary native stopped source requires a supported Linux host")
}
func (*StoppedOrdinarySessionV1) Check(context.Context, *SessionOperation) error {
	return sessionLeaseError("the original ordinary native stopped source is unavailable")
}
func (*StoppedOrdinarySessionV1) Close() error { return nil }
func (*StoppedOrdinarySessionV1) checkStoppedOrdinaryNativeFactsV1(context.Context) error {
	return sessionLeaseError("the original ordinary native stopped source is unavailable")
}
func (*StoppedOrdinarySessionV1) ordinaryScopeV1() string { return "" }

func managedOrdinaryEnvironmentV1(unit string) []string { return []string{unitEnv + "=" + unit} }

// OrdinaryPurgePreparationV2 has no native producer on this platform.
type OrdinaryPurgePreparationV2 struct{}

// PrepareOrdinaryPurgeForOperationV2 requires a supported Linux host.
func PrepareOrdinaryPurgeForOperationV2(context.Context, *SessionOperation) (*OrdinaryPurgePreparationV2, error) {
	return nil, sessionLeaseError("active ordinary preparation requires a supported Linux host")
}

// Check requires the original supported native producer.
func (*OrdinaryPurgePreparationV2) Check(context.Context, *SessionOperation) error {
	return sessionLeaseError("the active ordinary native producer is unavailable")
}

// Quiesce requires the original supported native creator.
func (*OrdinaryPurgePreparationV2) Quiesce(context.Context, *SessionOperation) error {
	return sessionLeaseError("the ordinary native pause producer is unavailable")
}

// CheckQuiesced requires the original supported native observation.
func (*OrdinaryPurgePreparationV2) CheckQuiesced(context.Context, *SessionOperation) error {
	return sessionLeaseError("the ordinary native pause observation is unavailable")
}

// Restore requires the original supported native creator.
func (*OrdinaryPurgePreparationV2) Restore(context.Context, *SessionOperation) error {
	return sessionLeaseError("the ordinary native restoration producer is unavailable")
}

// CommitStop requires the original supported native stopped producer.
func (*OrdinaryPurgePreparationV2) CommitStop(context.Context, *SessionOperation) (*StoppedOrdinarySessionV1, error) {
	return nil, sessionLeaseError("the ordinary native stop producer is unavailable")
}

// Close has no native resources on this platform.
func (*OrdinaryPurgePreparationV2) Close() error { return nil }
