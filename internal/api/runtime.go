package api

import (
	"fmt"
	"strings"
	"time"
)

// Images fetch modes for [images] fetch in config.toml and --images on
// host add / host prepare / host upgrade (isolation spec 14.9.3).
const (
	ImagesFetchAuto   = "auto"
	ImagesFetchHost   = "host"
	ImagesFetchLaptop = "laptop"
)

// ParseImagesFetch validates an [images] fetch or --images value. An empty
// string defaults to "auto".
func ParseImagesFetch(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "", ImagesFetchAuto:
		return ImagesFetchAuto, nil
	case ImagesFetchHost:
		return ImagesFetchHost, nil
	case ImagesFetchLaptop:
		return ImagesFetchLaptop, nil
	default:
		return "", fmt.Errorf("unknown images fetch mode %q", raw)
	}
}

// NormalizeSHA256Hex validates and normalises a 64-character SHA-256 hex
// digest (with optional "sha256:" prefix) to lowercase hex.
func NormalizeSHA256Hex(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "sha256:"))
	if len(s) != 64 {
		return "", false
	}
	for i := range len(s) {
		c := s[i]
		isDigit := c >= '0' && c <= '9'
		isHex := c >= 'a' && c <= 'f'
		if !isDigit && !isHex {
			return "", false
		}
	}
	return s, true
}

// ValidRuntimeLabel reports whether label is a safe runtime manifest filename
// stem (e.g. "2026.11.0"), containing no path separators or traversal.
func ValidRuntimeLabel(label string) bool {
	if label == "" || len(label) > 64 || label == "." || label == ".." {
		return false
	}
	if strings.HasPrefix(label, ".") || strings.HasPrefix(label, "-") {
		return false
	}
	for i := range len(label) {
		c := label[i]
		ok := (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '.' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

// RuntimeAsset describes one trusted runtime artefact named in a release
// manifest (isolation spec 14.9.1 and 14.9.3).
type RuntimeAsset struct {
	Kind       string `json:"kind"`                  // "vmm", "kernel", "tools", "gvisor", "launch"
	Name       string `json:"name"`                  // e.g. "cloud-hypervisor", "guest", "tools", "runsc"
	Version    string `json:"version,omitempty"`     // e.g. "55.0", "6.18.62"
	Arch       string `json:"arch,omitempty"`        // "amd64", "arm64"
	SHA256     string `json:"sha256"`                // 64-char lowercase hex
	Size       int64  `json:"size"`                  // exact byte size (> 0)
	URL        string `json:"url,omitempty"`         // HTTPS URL on the project's release
	RootHash   string `json:"root_hash,omitempty"`   // dm-verity root hash (tools)
	Salt       string `json:"salt,omitempty"`        // dm-verity salt (tools)
	DataBlocks uint64 `json:"data_blocks,omitempty"` // dm-verity data blocks (tools)
	HostKernel string `json:"host_kernel,omitempty"` // host kernel constraint (e.g. ">=6.8")
}

// Executable reports whether this runtime artefact must be stored with mode
// 0555 rather than 0444 (isolation spec 14.5: VMM and runsc are 0555).
func (a RuntimeAsset) Executable() bool {
	return a.Kind == "vmm" || a.Kind == "gvisor"
}

// RuntimeManifest describes the set of runtime artefacts shipped with one
// release label (isolation spec 14.9.1).
type RuntimeManifest struct {
	Release string         `json:"release,omitempty"`
	Runtime string         `json:"runtime"`
	Items   []RuntimeAsset `json:"items,omitempty"`
}

// ItemsForArch returns the runtime items in m matching arch (or items with an
// empty Arch).
func (m RuntimeManifest) ItemsForArch(arch string) []RuntimeAsset {
	var out []RuntimeAsset
	for _, it := range m.Items {
		if arch == "" || it.Arch == "" || it.Arch == arch {
			out = append(out, it)
		}
	}
	return out
}

// RuntimeFetchArgs is the request payload for runtime.fetch (spec 14.9.3).
// LabelItems, when non-empty, gives the full set of items belonging to Runtime
// so the host records the complete label manifest even when Items lists only
// the missing files.
type RuntimeFetchArgs struct {
	Release    string         `json:"release,omitempty"`
	Runtime    string         `json:"runtime,omitempty"`
	Items      []RuntimeAsset `json:"items"`
	LabelItems []RuntimeAsset `json:"label_items,omitempty"`
}

// RuntimeInstallEntry names one part file uploaded by the laptop into
// artefacts/incoming/ and its expected release-manifest entry.
type RuntimeInstallEntry struct {
	Asset RuntimeAsset `json:"asset"`
	Part  string       `json:"part"` // filename only inside artefacts/incoming/
}

// RuntimeInstallArgs is the request payload for runtime.install (spec 14.9.3).
// LabelItems, when non-empty, gives the full set of items belonging to Runtime
// so the host records the complete label manifest even when Items lists only
// the newly uploaded files.
type RuntimeInstallArgs struct {
	Release    string                `json:"release,omitempty"`
	Runtime    string                `json:"runtime,omitempty"`
	Items      []RuntimeInstallEntry `json:"items"`
	LabelItems []RuntimeAsset        `json:"label_items,omitempty"`
}

// RuntimeInstalledFile describes one runtime file verified and stored on a host.
type RuntimeInstalledFile struct {
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	Path           string `json:"path"`
	DurationMS     int64  `json:"duration_ms,omitempty"`
	AlreadyPresent bool   `json:"already_present,omitempty"`
}

// RuntimeInstallResult is the response of runtime.fetch and runtime.install.
type RuntimeInstallResult struct {
	Runtime   string                 `json:"runtime,omitempty"`
	Installed []RuntimeInstalledFile `json:"installed"`
}

// RuntimeTransfer reports live download progress for one in-flight runtime file.
type RuntimeTransfer struct {
	SHA256 string `json:"sha256"`
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	Size   int64  `json:"size"`
}

// RuntimeStatusResult is the response of runtime.status (spec 14.9.3).
type RuntimeStatusResult struct {
	Runtimes []string          `json:"runtimes,omitempty"`
	Files    map[string]int64  `json:"files,omitempty"`
	Active   []RuntimeTransfer `json:"active,omitempty"`
}

// StoredRuntimeLabel is the JSON written to artefacts/runtime/<label>.json on
// a host (isolation spec 14.5).
type StoredRuntimeLabel struct {
	Runtime    string         `json:"runtime"`
	Release    string         `json:"release,omitempty"`
	RecordedAt time.Time      `json:"recorded_at"`
	Items      []RuntimeAsset `json:"items"`
}
