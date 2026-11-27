package host

// Worktree descriptions retain future fields so that an old reader cannot
// silently discard another resource role before its native removal checks.
func (git *GitInfo) UnmarshalJSON(data []byte) error {
	type plain GitInfo
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"repo_root", "worktree", "branch", "base_ref", "base_sha", "created_by_us"})
	*git = GitInfo(value)
	git.extra = extra
	return err
}

func (git GitInfo) MarshalJSON() ([]byte, error) {
	type plain GitInfo
	return encodeFootprint(plain(git), git.extra)
}
