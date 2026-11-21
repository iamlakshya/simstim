//go:build linux

package microvm

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/proto"
)

func (phase *vmmExecPhaseV1) acceptReadonlyDiskV2(message runnerMessage, received *proto.UnixControlV1) error {
	phase.mu.Lock()
	defer phase.mu.Unlock()
	if !phase.readonly || phase.completed || phase.disk != nil || message.Session != phase.session || message.Driver != phase.driver {
		return fmt.Errorf("the original read-only disk belongs to another native phase")
	}
	actual, parent, err := phase.command.LoanVMMReadonlyDiskV2(phase.ctx, phase.source, phase.control, received)
	phase.disk, phase.diskParent = actual, parent // Adopt BOTH before any error branch.
	if actual != nil {
		err = errors.Join(err, phase.snapshotOwner.adoptReadonlyDiskV2(phase, actual))
	}
	return err
}

func (phase *vmmExecPhaseV1) runReadonlyV1(ctx context.Context, controller *runnerControl) error {
	if !phase.readonly {
		return fmt.Errorf("the original phase is not read-only")
	}
	if err := phase.command.ContinueVMMReadonlyInitV1(ctx, phase.source); err != nil {
		return err
	}
	if err := phase.command.WaitVMMReadonlyFinalV1(ctx, phase.source); err != nil {
		return err
	}
	if err := phase.command.FinishVMMExecModeV1(ctx, phase.source); err != nil {
		return err
	}
	if err := proto.SendUnixControlV1(phase.control, []byte("VMMROEXEC-000001"), nil); err != nil {
		return err
	}
	phase.mu.Lock()
	phase.completed = true
	phase.mu.Unlock()
	return phase.closePhaseReaderV1()
}

func (phase *vmmExecPhaseV1) closeReadonlyDiskV1() error {
	// Join original snapshot users before taking phase.mu: a finishing user
	// may still need that mutex for its final native source check.
	phase.snapshotOwner.fenceBorrowersV2()
	phase.mu.Lock()
	defer phase.mu.Unlock()
	if phase.diskParent != nil {
		if phase.parentRetirement == nil {
			phase.parentRetirement = &microVMFileRetirementV2{file: phase.diskParent}
		}
		if err := phase.parentRetirement.closeV2(); err != nil {
			return err
		}
		phase.diskParent = nil
	}
	if phase.disk == nil {
		return nil
	}
	if phase.diskRetirement == nil {
		phase.diskRetirement = &microVMFileRetirementV2{file: phase.disk}
	}
	if err := phase.diskRetirement.closeV2(); err != nil {
		return err
	}
	phase.disk = nil
	return nil
}

// Control EOF is a cancellation trigger only. The original actual C handle
// decides whether a kill is needed; its physical proof still gates release.
func (phase *vmmExecPhaseV1) readonlyControlEndedV1() {
	if phase == nil || !phase.readonly {
		return
	}
	ended, err := phase.command.ReadonlyInitEndedV1(phase.source)
	if err != nil || !ended {
		_ = phase.command.Signal(os.Kill)
	}
}

func (phase *vmmExecPhaseV1) isReadonlyV1() bool { return phase != nil && phase.readonly }
func (phase *vmmExecPhaseV1) readonlyExitV1() (int, string, error) {
	return phase.command.ReadonlyExitCodeV1(phase.source)
}

func (phase *vmmExecPhaseV1) closePhaseReaderV1() error {
	if phase.reader == nil {
		return nil
	}
	if phase.readerRetirement == nil {
		phase.readerRetirement = &microVMFileRetirementV2{file: phase.reader}
	}
	if err := phase.readerRetirement.closeV2(); err != nil {
		return err
	}
	phase.reader = nil
	return nil
}
func (phase *vmmExecPhaseV1) closePhaseWriterV1() error {
	if phase.writer == nil {
		return nil
	}
	if phase.writerRetirement == nil {
		phase.writerRetirement = &microVMFileRetirementV2{file: phase.writer}
	}
	if err := phase.writerRetirement.closeV2(); err != nil {
		return err
	}
	phase.writer = nil
	return nil
}
