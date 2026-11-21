//go:build !linux

package host

type noneMetadataFirstRetirementV1 struct{}

func (plan *noneMetadataCleanupV1) retireFirstV1() error {
	return sessionLeaseError("native None metadata lease retirement requires its supported Linux close profile")
}
