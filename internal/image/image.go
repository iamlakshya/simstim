// Package image prepares digest-pinned OCI images for microvm roots.
// Registry bytes are untrusted. Only the client chooses image digests.
package image

import (
	"fmt"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

// Limits bound work while bytes are read, including discarded layer entries.
const (
	// MaxCompressed bounds the sum of compressed layer bytes.
	MaxCompressed int64 = 8 << 30
	// MaxUncompressed bounds all decoded tar bytes, including discarded entries.
	MaxUncompressed int64 = 16 << 30
	// MaxEntries bounds the merged tree, including implicit directories.
	MaxEntries = 1_000_000
	// MaxLayers bounds an OCI image's layer list.
	MaxLayers = 128
	// MaxManifest bounds each manifest or index before parsing.
	MaxManifest int64 = 4 << 20
	// MaxChunk bounds one laptop-to-host image copy frame.
	MaxChunk = 64 << 10
)

// Layer describes one compressed layer authorised by a platform manifest.
type Layer struct {
	Digest    string `json:"digest"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
}

// Request carries client-owned pins. RootHash is an optional check value.
type Request struct {
	Ref            string       `json:"ref"`
	Digest         string       `json:"digest"`
	PlatformDigest string       `json:"platform_digest"`
	Arch           string       `json:"arch"`
	RootHash       string       `json:"root_hash,omitempty"`
	DataBlocks     int64        `json:"data_blocks,omitempty"`
	Transfer       string       `json:"transfer,omitempty"`
	SavedHost      *SavedHostV1 `json:"saved_host,omitempty"`
}

// Record is host-owned metadata outside every guest's writable filesystem.
type Record struct {
	Ref            string       `json:"ref"`
	Digest         string       `json:"digest"`
	PlatformDigest string       `json:"platform_digest"`
	Arch           string       `json:"arch"`
	RootHash       string       `json:"root_hash"`
	DataBlocks     int64        `json:"data_blocks"`
	Size           int64        `json:"size"`
	ConvertedAt    time.Time    `json:"converted_at"`
	UsedAt         time.Time    `json:"used_at"`
	SavedHost      *SavedHostV1 `json:"saved_host,omitempty"`
	SavedAliases   []string     `json:"saved_aliases,omitempty"`
}

// Install is a bounded phase of a connection-owned laptop transfer.
type Install struct {
	Action  string   `json:"action"`
	Request *Request `json:"request,omitempty"`
	Token   string   `json:"token,omitempty"`
	Digest  string   `json:"digest,omitempty"`
	Offset  int64    `json:"offset,omitempty"`
	Data    []byte   `json:"data,omitempty"`
}

// Progress reports only a connection's own transfer.
type Progress struct {
	Token    string `json:"token"`
	Received int64  `json:"received"`
	Total    int64  `json:"total"`
}

// StatusRequest selects a connection-owned progress token or a cached image.
// A cache check never fetches bytes and accepts only client-owned pins.
type StatusRequest struct {
	Token   string   `json:"token,omitempty"`
	Request *Request `json:"request,omitempty"`
}

// CacheStatus reports a locally checked host image, without a registry lookup.
type CacheStatus struct {
	Present bool    `json:"present"`
	Image   *Record `json:"image,omitempty"`
}

// ValidDigest accepts SHA-256 digests in OCI's canonical spelling.
func ValidDigest(s string) bool { return len(s) == 71 && s[:7] == "sha256:" && supply.IsHex64(s[7:]) }

// Validate checks request authority before filesystem or registry operations.
func (r Request) Validate() error {
	if r.SavedHost != nil {
		return validateSavedRequest(r)
	}
	if r.Transfer != "" && !supply.IsHex64(r.Transfer+r.Transfer) {
		return badLayer("invalid image transfer identifier")
	}
	ref, err := ParseReference(r.Ref)
	if err != nil {
		return err
	}
	if ValidDigest(ref.Identifier) && ref.Identifier != r.Digest {
		return badLayer("the explicit image reference differs from the client's digest")
	}
	if !ValidDigest(r.Digest) || !ValidDigest(r.PlatformDigest) {
		return badLayer("an image needs canonical manifest and platform SHA-256 digests")
	}
	if r.Arch != "amd64" && r.Arch != "arm64" {
		return badLayer("an image needs linux/amd64 or linux/arm64")
	}
	if (r.RootHash == "") != (r.DataBlocks == 0) || r.RootHash != "" && !supply.IsHex64(r.RootHash) || r.DataBlocks < 0 || r.DataBlocks > MaxUncompressed/4096 {
		return badLayer("the image root check has invalid metadata")
	}
	return nil
}

// CheckRecord validates a host's result against client-owned request fields.
// A reply never changes the OCI digest pin.
func CheckRecord(request Request, record Record) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := checkSavedSelection(request, record); err != nil {
		return err
	}
	if record.Ref != request.Ref || record.Digest != request.Digest || record.PlatformDigest != request.PlatformDigest || record.Arch != request.Arch || !supply.IsHex64(record.RootHash) || record.DataBlocks <= 0 || record.DataBlocks > MaxUncompressed/4096 || record.Size != convertedBound(record.DataBlocks*4096) {
		return &proto.Error{Code: "image_protocol", Message: "the host returned image metadata outside the client's request", Hint: "Upgrade the helper and pull the image again."}
	}
	if request.RootHash != "" && (request.RootHash != record.RootHash || request.DataBlocks != record.DataBlocks) {
		return rootCheck(request, record)
	}
	return nil
}

func badLayer(format string, args ...any) error {
	return &proto.Error{Code: "image_bad_layer", Message: fmt.Sprintf(format, args...), Hint: "Rebuild the image, or use another one."}
}
func tooLarge(format string, args ...any) error {
	return &proto.Error{Code: "image_too_large", Message: fmt.Sprintf(format, args...), Hint: "Use a smaller image."}
}
func mismatch(what string) error {
	return &proto.Error{Code: "digest_mismatch", Message: what + " does not match the client's digest", Hint: "Pull the image again from a registry you trust."}
}
