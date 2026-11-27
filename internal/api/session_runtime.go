package api

// SessionRuntimeInfoV1 is advisory running-session metadata. It supplies no
// original signed manifest, held bytes, Need, native operation or start grant.
type SessionRuntimeInfoV1 struct {
	Version int                    `json:"v"`
	Label   string                 `json:"label"`
	Items   []SessionRuntimeItemV1 `json:"items"`
}

type SessionRuntimeItemV1 struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Arch    string `json:"arch"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}
