package api

// BaselineParentKeys is the closed vocabulary of parent directory observations.
// These are roles under HOME or the configured XDG bases, never delete paths.
func BaselineParentKeys() []string {
	return []string{"local", "local_share", "local_state", "config", "systemd", "systemd_user", "default_wants", "timers_wants", "terminfo", "xdg_data", "xdg_state", "xdg_config"}
}

// BaselineParentClaim binds original absence to a fixed role's path digest.
// The host derives the path itself and rejects environment drift.
type BaselineParentClaim struct {
	Key        string `json:"key"`
	PathSHA256 string `json:"path_sha256"`
}
