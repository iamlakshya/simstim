package host

import "os"

// The closed installer shares the existing canonical control/place locks and
// exact active publisher receipt. It owns no alternate lock or request model.
func withInstallerControlV1(owner *stateMetadataPublisher, change func(*placementControl, *os.Root) error) error {
	if owner == nil || owner.operation == nil || owner.operation.owner == nil || !owner.operation.owner.installer {
		return placementError("the installation has no original closed publisher")
	}
	return withPlacementRoot(owner.ctx, false, func(control *placementControl, guard *os.Root) error {
		if err := checkStateMetadataPublisher(owner.ctx, control, owner.operation); err != nil {
			return err
		}
		return change(control, guard)
	}, true)
}
