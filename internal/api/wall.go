package api

// WallProbe is one driver's probe, as the client sees it.
type WallProbe struct {
	// CanPrepare permits explicit client-pinned first construction; it is not compatibility readiness.
	CanPrepare bool `json:"can_prepare,omitempty"`
	Driver  string            `json:"driver"`
	Level   string            `json:"level"`
	OK      bool              `json:"ok"`
	Reason  string            `json:"reason,omitempty"`
	Fix     string            `json:"fix,omitempty"`
	Version string            `json:"version,omitempty"`
	Facts   map[string]string `json:"facts,omitempty"`
}

// WallInfo is a session's wall, in its row.
type WallInfo struct {
	Level          string `json:"level"`
	Driver         string `json:"driver"`
	Version        string `json:"version,omitempty"`
	Floor          string `json:"floor,omitempty"`
	DowngradedFrom string `json:"downgraded_from,omitempty"`
	Preset         string `json:"preset,omitempty"`
}
