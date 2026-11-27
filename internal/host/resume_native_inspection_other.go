//go:build !linux

package host

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

type WarmResumeV1 struct{}
type ColdResumeV1 struct{}

func InspectResumeForOperationV1(context.Context, *SessionOperation) (*WarmResumeV1, *ColdResumeV1, error) {
	return nil, nil, sessionLeaseError("native resume inspection requires a supported Linux host")
}
func (*WarmResumeV1) Check(context.Context, *SessionOperation) error {
	return sessionLeaseError("native warm resume requires a supported Linux host")
}
func (*WarmResumeV1) Resume(context.Context, *SessionOperation, api.ResumeArgs) (*api.ResumeResult, error) {
	return nil, sessionLeaseError("native warm resume requires a supported Linux host")
}
func (*WarmResumeV1) Close() error { return nil }
func (*ColdResumeV1) Check(context.Context, *SessionOperation) error {
	return sessionLeaseError("native cold resume requires a supported Linux host")
}
func (*ColdResumeV1) CheckManifest(context.Context, *SessionOperation, *Manifest) error {
	return sessionLeaseError("native cold resume requires a supported Linux host")
}
func (*ColdResumeV1) Close() error { return nil }

func (*ColdResumeV1) Resume(context.Context, *SessionOperation, api.ResumeArgs) (*api.ResumeResult, error) {
	return nil, sessionLeaseError("native cold resume requires a supported Linux host")
}
func (*ColdResumeV1) SettleForCancellationV1(context.Context, *SessionOperation) error {
	return sessionLeaseError("native cold cancellation requires a supported Linux host")
}
