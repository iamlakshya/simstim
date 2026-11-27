package api

// DrainArgs changes a host setting. It grants no stop, boot or session role.
type DrainArgs struct {
	On     bool   `json:"on"`
	Reason string `json:"reason"`
}

// DrainState is a display copy of the current user drain setting.
type DrainState struct {
	On     bool   `json:"on"`
	Reason string `json:"reason"`
}
