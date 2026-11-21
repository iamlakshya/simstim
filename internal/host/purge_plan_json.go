package host

func (resource *purgeResource) UnmarshalJSON(data []byte) error {
	type plain purgeResource
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"root", "relative", "ref", "directory", "quarantine", "sha256", "item"})
	*resource = purgeResource(value)
	resource.extra = extra
	return err
}
func (resource purgeResource) MarshalJSON() ([]byte, error) {
	type plain purgeResource
	return encodeFootprint(plain(resource), resource.extra)
}
func (plan *purgeSessionPlan) UnmarshalJSON(data []byte) error {
	type plain purgeSessionPlan
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"id", "name", "git", "runtime", "holder_binary", "wall", "directory", "worktree", "worktree_capture", "git_tip", "git_released", "stopped", "cleaned"})
	*plan = purgeSessionPlan(value)
	plan.extra = extra
	return err
}
func (plan purgeSessionPlan) MarshalJSON() ([]byte, error) {
	type plain purgeSessionPlan
	return encodeFootprint(plain(plan), plan.extra)
}
