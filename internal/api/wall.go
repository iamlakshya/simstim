package api

// WallProbe is one wall driver's probe result, as the client sees it
// (isolation spec 5.1 and 10.6).
type WallProbe struct {
	Driver  string            `json:"driver"`
	Level   string            `json:"level"`
	OK      bool              `json:"ok"`
	Reason  string            `json:"reason,omitempty"`
	Fix     string            `json:"fix,omitempty"`
	Version string            `json:"version,omitempty"`
	Facts   map[string]string `json:"facts,omitempty"`
}
