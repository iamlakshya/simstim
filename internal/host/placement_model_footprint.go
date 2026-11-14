package host

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Footprint is an intent recorded before a change outside the product folders.
// Owned identifies the inode prepared by the producer, including an intent
// whose publication succeeded before its completion receipt could be saved.
type Footprint struct {
	ID          string                      `json:"id"`
	Root        string                      `json:"root,omitempty"`
	Original    *footprintRef               `json:"original,omitempty"`
	Backup      string                      `json:"backup,omitempty"`
	Kind        string                      `json:"kind"`
	Path        string                      `json:"path,omitempty"`
	Before      string                      `json:"before"`
	After       string                      `json:"after,omitempty"`
	At          time.Time                   `json:"at"`
	Status      string                      `json:"status"`
	Owned       *footprintRef               `json:"owned,omitempty"`
	Directory   *purgeRef                   `json:"directory,omitempty"`
	PreparedRef *purgeRef                   `json:"prepared_ref,omitempty"`
	Prepared    string                      `json:"prepared,omitempty"`
	Quarantine  string                      `json:"quarantine,omitempty"`
	Link        *purgeRef                   `json:"owned_link,omitempty"`
	LinkTarget  string                      `json:"link_target,omitempty"`
	Privileged  bool                        `json:"privileged,omitempty"`
	Pending     map[string]footprintPending `json:"pending,omitempty"`
	Previous    string                      `json:"previous,omitempty"`
	extra       map[string]json.RawMessage
}

type footprintLedger struct {
	Version int         `json:"version"`
	HostID  string      `json:"host_id"`
	UID     int         `json:"uid"`
	Entries []Footprint `json:"entries"`
	extra   map[string]json.RawMessage
}

type footprintRef struct {
	Device  uint64    `json:"device"`
	Inode   uint64    `json:"inode"`
	UID     uint64    `json:"uid"`
	Mode    uint32    `json:"mode"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mtime"`
	SHA256  string    `json:"sha256"`
	extra   map[string]json.RawMessage
}

type footprintPending struct {
	Original string   `json:"original"`
	Ref      purgeRef `json:"ref"`
	SHA256   string   `json:"sha256,omitempty"`
}

// UnmarshalJSON retains future footprint witnesses without adopting them.
func (r *footprintRef) UnmarshalJSON(data []byte) error {
	type plain footprintRef
	var value plain
	extra, err := decodeFootprint(data, &value, []string{"device", "inode", "uid", "mode", "size", "mtime", "sha256"})
	*r = footprintRef(value)
	r.extra = extra
	return err
}

// MarshalJSON preserves future footprint witness fields.
func (r footprintRef) MarshalJSON() ([]byte, error) {
	type plain footprintRef
	return encodeFootprint(plain(r), r.extra)
}

func footprintNonce() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("make footprint operation identity: %w", err)
	}
	return hex.EncodeToString(nonce[:]), nil
}

func footprintDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
