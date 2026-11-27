//go:build !linux

package host

import "context"

type resumePreparationLeaseV1 struct{}

func beginResumePreparationV1(context.Context, *SessionOperation, *ColdResumeV1, *Manifest, string) (*resumePreparationLeaseV1, error) {
	return nil, resumePreparationDataErrorV1()
}
func (*resumePreparationLeaseV1) Check(context.Context, *SessionOperation, *ColdResumeV1) error {
	return resumePreparationDataErrorV1()
}
func (*resumePreparationLeaseV1) TransferToStarting(context.Context, *SessionOperation, *ColdResumeV1, *Manifest) error {
	return resumePreparationDataErrorV1()
}
func (*resumePreparationLeaseV1) Cancel(context.Context, *SessionOperation, *ColdResumeV1) error {
	return resumePreparationDataErrorV1()
}
func takeoverResumePreparationNativeLockedV1(context.Context, *SessionOperation, *ColdResumeV1, *placementControl) error {
	return resumePreparationDataErrorV1()
}
func checkResumePreparationStartLockedV1(context.Context, *SessionOperation, *Manifest, *placementControl) error {
	return resumePreparationDataErrorV1()
}
func reserveRestartMemoryControlLockedV1(ctx context.Context, operation *SessionOperation, next *Manifest, control *placementControl) (*sessionFileLease, error) {
	if next != nil && next.ResumePreparation != nil {
		return nil, resumePreparationDataErrorV1()
	}
	return reserveRestartMemoryLockedV1(ctx, operation, next)
}
