//go:build linux

package microvm

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/jail"
)

type runnerExecModeV1 struct {
	stage         *jail.VMMExecLeaseStageV1
	lease, writer *os.File
	closed        bool
}

func captureRunnerExecModeV1(ctx context.Context, version int, program *os.File) (*runnerExecModeV1, error) {
	stage, err := jail.CaptureVMMExecLeaseStageV1()
	if err != nil {
		return nil, err
	}
	if version == 1 {
		if stage != nil {
			stage.Close()
			return nil, fmt.Errorf("sealed runner plan attempted to consume a native mode phase")
		}
		return nil, nil
	}
	if version != 2 || stage == nil {
		return nil, fmt.Errorf("direct runner plan has no actual private jail mode source")
	}
	lease, writer, err := stage.RetainForNextHelperV1(ctx, program)
	if err != nil {
		stage.Close()
		return nil, err
	}
	return &runnerExecModeV1{stage: stage, lease: lease, writer: writer}, nil
}

func (mode *runnerExecModeV1) Close() error {
	if mode == nil || mode.closed {
		return nil
	}
	mode.closed = true
	// Only actual host continuation (after native adoption/projection+Arm),
	// or this trusted ancestor's actual death/error cleanup, drops these copies.
	return errors.Join(mode.lease.Close(), mode.writer.Close(), mode.stage.Close())
}
