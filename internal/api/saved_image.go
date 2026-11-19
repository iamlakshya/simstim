package api

// SavedImageInfoV1 is display-only original saved-root metadata. It grants no
// local pin, native store access, image selection or execution authority.
type SavedImageInfoV1 struct {
	Version int    `json:"v"`
	Ref     string `json:"ref"`
	Digest  string `json:"digest"`
	HostID  string `json:"host_id"`
}
