//go:build !linux

package host

import (
	"context"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func (*bridge) prepareRewindV1(*bridgeRewindServiceV1, proto.Request) (*api.RewindPrepareResultV1, error) {
	return nil, UserError("rewind_off", "native disk rewind needs a supported Linux host", "Use a Linux microvm whose filesystem supports file cloning.")
}
func (*bridgeRewindPreparationV1) checkPreparationV1(context.Context) error {
	return rewindFailureV1("native rewind is unavailable on this host")
}
func (*bridgeRewindPreparationV1) commitRewindV1(proto.Request, api.RewindCommitArgsV1) (*api.RewindResult, error) {
	return nil, rewindFailureV1("native rewind is unavailable on this host")
}
func (*bridgeRewindPreparationV1) checkAccountingOriginV1(context.Context, *SessionOperation, *ColdResumeV1) error {
	return rewindFailureV1("native rewind accounting is unavailable on this host")
}
func (*bridgeRewindPreparationV1) closeOwnedV1() error { return nil }

func (*bridgeRewindPreparationV1) captureStartupDeliveryV1(context.Context, *ColdResumeV1, *Manifest, api.ResumeArgs) error {
	return rewindFailureV1("native rewind delivery is unavailable on this host")
}

func (*bridgeRewindPreparationV1) checkColdRestartCohortV1(context.Context, *api.SandboxSnapshot) error {
	return rewindFailureV1("native rewind retry is unavailable on this host")
}
