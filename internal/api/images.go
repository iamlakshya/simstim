package api

import "time"

// ImageRecord describes one converted OCI image in the host's store.
type ImageRecord struct {
	Ref            string    `json:"ref"`
	Digest         string    `json:"digest"`
	PlatformDigest string    `json:"platform_digest"`
	Arch           string    `json:"arch"`
	RootHash       string    `json:"root_hash"`
	DataBlocks     uint64    `json:"data_blocks"`
	Size           int64     `json:"size"`
	ConvertedAt    time.Time `json:"converted_at"`
	UsedAt         time.Time `json:"used_at"`
}

// ImagePullArgs asks a host to fetch and convert one OCI image by digest.
type ImagePullArgs struct {
	Ref              string `json:"ref"`
	Digest           string `json:"digest"`
	Arch             string `json:"arch,omitempty"`
	ExpectedRootHash string `json:"expected_root_hash,omitempty"`
}

// ImageRemoveArgs asks a host to remove one stored image by digest or prefix.
type ImageRemoveArgs struct {
	Digest string `json:"digest"`
}

// AgentsDiskRecord describes the host's read-only agents EROFS disk.
type AgentsDiskRecord struct {
	Digest  string    `json:"digest"`
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	Agents  []string  `json:"agents"`
	BuiltAt time.Time `json:"built_at"`
}
