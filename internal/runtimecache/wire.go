package runtimecache

import "github.com/iamlakshya/simstim/internal/supply"

// RuntimeRequest carries runtime entries and separate image-default retention
// metadata authorised by this client.
// A host-reported digest is never used to construct this manifest.
type RuntimeRequest struct {
	Manifest      supply.Manifest `json:"manifest"`
	ImageDefaults *[]supply.Item  `json:"image_defaults,omitempty"`
	Transfer      string          `json:"transfer,omitempty"`
}

// RuntimeInstall is one bounded phase of a laptop copy over the bridge.
// Only begin and commit carry a manifest. Only commit carries image defaults.
// Other phases use an opaque token owned by the bridge and never accept a
// caller-supplied filesystem path.
type RuntimeInstall struct {
	Action        string           `json:"action"`
	Manifest      *supply.Manifest `json:"manifest,omitempty"`
	ImageDefaults *[]supply.Item   `json:"image_defaults,omitempty"`
	SHA256        string           `json:"sha256,omitempty"`
	Part          string           `json:"part,omitempty"`
	Offset        int64            `json:"offset,omitempty"`
	Data          []byte           `json:"data,omitempty"`
}

// RuntimePart is an unguessable connection-owned token for a new transfer.
type RuntimePart struct {
	Part string `json:"part"`
}
