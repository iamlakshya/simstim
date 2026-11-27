//go:build !linux

package host

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

type NoneColdResumeV1 struct{}

func captureNoneColdResumeV1(context.Context, *bridgeResumePreparationV1) (*NoneColdResumeV1, error) {
	return nil, sessionLeaseError("the original None continuation needs a supported Linux host")
}
func (*NoneColdResumeV1) Check(context.Context, *SessionOperation) error {
	return sessionLeaseError("the original None continuation is unavailable")
}
func (*NoneColdResumeV1) CheckManifest(context.Context, *SessionOperation, *Manifest) error {
	return sessionLeaseError("the original None continuation is unavailable")
}
func (*NoneColdResumeV1) Resume(context.Context, *SessionOperation, api.ResumeArgs) (*api.ResumeResult, error) {
	return nil, sessionLeaseError("the original None continuation is unavailable")
}
func (*NoneColdResumeV1) Close() error { return nil }
func checkNoneColdRestartWriterV1(_ context.Context, operation *SessionOperation, _, _ *Manifest) error {
	if operation != nil && operation.noneCold != nil {
		return sessionLeaseError("the original None continuation needs a Linux host")
	}
	return nil
}
func retainNoneColdRestartWriterV1(_ context.Context, operation *SessionOperation, _ *Manifest) error {
	if operation != nil && operation.noneCold != nil {
		return sessionLeaseError("the original None continuation needs a Linux host")
	}
	return nil
}

func (*NoneColdResumeV1) prepareManagerBootstrapV1(context.Context) error {
	return sessionLeaseError("the original managed None continuation needs a supported Linux host")
}
