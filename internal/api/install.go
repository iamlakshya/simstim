package api

// InstallPlanV1 names only the client's carried helper roles. File bodies
// follow the bounded JSON header, in Items order, on the same original input.
// Neither a file name nor these digests issues runtime or boot authority.
type InstallPlanV1 struct {
	Version       int             `json:"v"`
	Nonce         string          `json:"nonce"`
	HelperVersion string          `json:"helper_version"`
	Arch          string          `json:"arch"`
	Items         []InstallItemV1 `json:"items"`
}

type InstallItemV1 struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
