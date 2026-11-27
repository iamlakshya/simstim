package proto

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// CtlBundlePrepare registers only a domain-checked Git bundle command.
// CtlBundleCancel releases its matching operation and command ownership.
const (
	CtlBundlePrepare = "bundle.prepare"
	CtlBundleCancel  = "bundle.cancel"
)

// BundleRef is one captured branch identity, never a filesystem path.
type BundleRef struct {
	Name string `json:"name"`
	OID  string `json:"oid"`
}

// CtlBundleArgs contains trusted supervisor choices for the fixed exporter.
// Count and digest are absent on first capture and exact on a full retry.
type CtlBundleArgs struct {
	ID          string   `json:"id"`
	Primary     string   `json:"primary"`
	Cwd         string   `json:"cwd"`
	SeedHeads   []string `json:"seed_heads,omitempty"`
	HeadCount   int      `json:"head_count,omitempty"`
	HeadsDigest string   `json:"heads_digest,omitempty"`
	DefaultRef  string   `json:"default_ref,omitempty"`
	Full        bool     `json:"full,omitempty"`
}

// CtlBundleReady returns bounded untrusted guest metadata for one stream.
type CtlBundleReady struct {
	ID         string `json:"id"`
	Empty      bool   `json:"empty,omitempty"`
	Full       bool   `json:"full,omitempty"`
	DefaultRef string `json:"default_ref,omitempty"`
}

// BundleSnapshot binds every exact name and OID in a counted canonical list.
// It is a consistency check on a retained capture, never authority for refs.
func BundleSnapshot(heads []BundleRef) (int, string) {
	canonical := slices.Clone(heads)
	slices.SortFunc(canonical, func(a, b BundleRef) int {
		if order := strings.Compare(a.Name, b.Name); order != 0 {
			return order
		}
		return strings.Compare(a.OID, b.OID)
	})
	hash := sha256.New()
	hash.Write([]byte("bundle-head-snapshot-v1\x00"))
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(canonical)))
	hash.Write(size[:])
	for _, head := range canonical {
		for _, value := range []string{head.Name, head.OID} {
			binary.BigEndian.PutUint64(size[:], uint64(len(value)))
			hash.Write(size[:])
			hash.Write([]byte(value))
		}
	}
	return len(canonical), hex.EncodeToString(hash.Sum(nil))
}

// BundleRequestFits checks the actual encoded request against the existing
// 64 KiB ctl frame limit, including type, envelope, op and actual request ID.
// Callers that do not own the request ID may use zero only as a prefilter.
func BundleRequestFits(id uint64, args CtlBundleArgs) bool {
	payload, err := json.Marshal(args)
	if err != nil {
		return false
	}
	request, err := json.Marshal(Request{ID: id, Op: CtlBundlePrepare, Args: payload})
	return err == nil && len(request)+1 <= 64<<10
}

// BundleResponseFits checks the actual success envelope before it is written.
func BundleResponseFits(id uint64, ready CtlBundleReady) bool {
	payload, err := json.Marshal(ready)
	if err != nil {
		return false
	}
	response, err := json.Marshal(Response{ID: id, OK: true, Result: payload})
	return err == nil && len(response)+1 <= 64<<10
}
