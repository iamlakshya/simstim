//go:build linux

package host

// This is the common actual resource adapter, without any new source role.
type noneMetadataFirstRetirementV1 = nativeStartupRetirementsV1

func (plan *noneMetadataCleanupV1) retireFirstV1() error {
	if err := plan.firstRetirement.bindDescriptorOwnerV4(&plan.retirement); err != nil {
		return err
	}
	// plan.mu joins the only actual users before this first native call.
	_ = plan.firstRetirement.closeSessionLeaseV4(plan.first)
	return plan.firstRetirement.checkSessionLeaseV4(plan.first)
}
