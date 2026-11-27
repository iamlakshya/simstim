package api

import (
	"encoding/json"
	"io/fs"
	"time"
)

// PurgeNativeReference is opaque ownership data for one fixed guard role.
// It supplies no path and never authorises another host's files or key.
type PurgeNativeReference struct {
	Device   uint64    `json:"device"`
	Inode    uint64    `json:"inode"`
	UID      uint64    `json:"uid"`
	Mode     uint32    `json:"mode"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	extra    map[string]json.RawMessage
}

// PurgeGuardWitness is the prepared finalisation that the laptop saves before
// acknowledging journal deletion. The server checks every role and identity.
type PurgeGuardWitness struct {
	HostID       string               `json:"host_id"`
	Nonce        string               `json:"operation_nonce"`
	Scope        string               `json:"scope"`
	User         string               `json:"user"`
	Arch         string               `json:"arch"`
	Version      int                  `json:"version"`
	Token        string               `json:"token"`
	Root         PurgeNativeReference `json:"root"`
	Sentinel     PurgeNativeReference `json:"sentinel"`
	MarkerSHA256 string               `json:"marker_sha256,omitempty"`
	Marker       PurgeNativeReference `json:"marker"`
	extra        map[string]json.RawMessage
}

func isLowerHex(s string, wantLen int) bool {
	if len(s) != wantLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Known reports the closed current shape, without claiming native ownership.
func (w PurgeGuardWitness) Known() bool {
	if w.MarkerSHA256 != "" && !isLowerHex(w.MarkerSHA256, 64) {
		return false
	}
	refs := []PurgeNativeReference{w.Root, w.Sentinel, w.Marker}
	if !isLowerHex(w.HostID, 32) || !isLowerHex(w.Nonce, 32) || !isLowerHex(w.Scope, 64) || w.User == "" || (w.Arch != "amd64" && w.Arch != "arm64") || w.Version != 1 || !isLowerHex(w.Token, 32) || len(w.extra) != 0 {
		return false
	}
	for _, ref := range refs {
		if len(ref.extra) != 0 || ref.Inode == 0 || ref.Size < 0 || ref.Modified.IsZero() {
			return false
		}
	}
	return fs.FileMode(w.Root.Mode) == fs.ModeDir|0o700 && w.Sentinel.Mode == 0o600 && w.Sentinel.Size == 0 && w.Marker.Mode == 0o600 && w.Marker.Size >= 0 && w.Root.UID == w.Sentinel.UID && w.Root.UID == w.Marker.UID
}

// UnmarshalJSON preserves future native data while keeping it nonauthoritative.
func (ref *PurgeNativeReference) UnmarshalJSON(data []byte) error {
	type plain PurgeNativeReference
	var value plain
	extra, err := decodePurge(data, &value, []string{"device", "inode", "uid", "mode", "size", "modified"})
	*ref = PurgeNativeReference(value)
	ref.extra = extra
	return err
}

// MarshalJSON retains unknown native fields.
func (ref PurgeNativeReference) MarshalJSON() ([]byte, error) {
	type plain PurgeNativeReference
	return encodePurge(plain(ref), ref.extra)
}

// UnmarshalJSON preserves future witness data without granting authority.
func (w *PurgeGuardWitness) UnmarshalJSON(data []byte) error {
	type plain PurgeGuardWitness
	var value plain
	extra, err := decodePurge(data, &value, []string{"host_id", "operation_nonce", "scope", "user", "arch", "version", "token", "root", "sentinel", "marker", "marker_sha256"})
	*w = PurgeGuardWitness(value)
	w.extra = extra
	return err
}

// MarshalJSON retains unknown witness fields.
func (w PurgeGuardWitness) MarshalJSON() ([]byte, error) {
	type plain PurgeGuardWitness
	return encodePurge(plain(w), w.extra)
}

// Prepared reports an acknowledged marker snapshot, including its exact body.
// The stored pre-write identity alone is insufficient for a commit request.
func (w PurgeGuardWitness) Prepared() bool {
	return w.Known() && w.Marker.Size > 0 && isLowerHex(w.MarkerSHA256, 64)
}
