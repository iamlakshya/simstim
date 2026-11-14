package jail

// MountPoints parses a supplied Linux mountinfo snapshot with the SAME strict
// mount/path decoder used by jail backing checks. It performs no syscall,
// namespace operation, probe or privilege change. Stacked points are retained.
func MountPoints(text string) ([]string, error) {
	mounts, err := parseBackingMounts(text)
	if err != nil {
		return nil, err
	}
	points := make([]string, 0, len(mounts))
	for _, mount := range mounts {
		points = append(points, mount.point)
	}
	return points, nil
}
