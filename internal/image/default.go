package image

import (
	"fmt"
	"strings"
)

// DefaultName is the short name that selects the pinned official session image.
const DefaultName = "default"

// The default session image is the official Debian buildpack-deps:trixie-scm
// image (which ships git, curl and CA certificates), pinned by its OCI index
// digest (checked on 27 November 2026).
const (
	DefaultRef    = "docker.io/library/buildpack-deps:trixie-scm"
	DefaultDigest = "sha256:d524aa66d91264606eddea5b94af462fc3b969b84a1d4083e537a9ab84b579c5"
)

// DefaultPlatform records the pinned platform manifest and expected dm-verity
// check value for one architecture of the default session image.
type DefaultPlatform struct {
	Arch           string
	PlatformDigest string
	RootHash       string
	DataBlocks     uint64
}

// DefaultPlatforms holds the pinned per-architecture check values for the
// default session image. RootHash and DataBlocks are verified check values from
// converting the pinned digest with go-containerregistry and hcsshim tar2ext4.
var DefaultPlatforms = map[string]DefaultPlatform{
	"amd64": {
		Arch:           "amd64",
		PlatformDigest: "sha256:7a8e8efa733d02ed2cf13e2bcc9c249941bf3dd750e043043c734b770404b341",
		RootHash:       "1b6f5a96bd68460fb99dc592da0975429466b32e660f5b89dda33f3bd15f4ebf",
		DataBlocks:     97711,
	},
	"arm64": {
		Arch:           "arm64",
		PlatformDigest: "sha256:d1add053e30bd462538b1d608d977209d75d85df7af98d591c9b8fcd928455bf",
		RootHash:       "ef0584e31a0b9dd0340b9cd279639cefab2ecc0b0fb0b38a760cab9885fb2c1a",
		DataBlocks:     104228,
	},
}

// ResolveDefault maps "" or "default" to the pinned official session image.
// For any other reference it returns ref unchanged with isDefault false.
func ResolveDefault(ref string) (canonicalRef, pinnedDigest string, isDefault bool) {
	r := strings.TrimSpace(ref)
	if r == "" || r == DefaultName {
		return DefaultRef, DefaultDigest, true
	}
	return r, "", false
}

// DefaultForArch returns the pinned platform record for arch ("amd64" or "arm64").
func DefaultForArch(arch string) (DefaultPlatform, error) {
	p, ok := DefaultPlatforms[arch]
	if !ok {
		return DefaultPlatform{}, &Error{
			Code:    CodeNotFound,
			Message: fmt.Sprintf("the default image has no pinned build for architecture %q", arch),
			Hint:    "Use an amd64 or arm64 host, or pull a custom image for this architecture.",
		}
	}
	return p, nil
}

// DefaultPinnedHexes returns the hex platform digests of the default image, so
// garbage collection keeps the default image in the host store.
func DefaultPinnedHexes() []string {
	out := make([]string, 0, len(DefaultPlatforms))
	for _, p := range DefaultPlatforms {
		if hex, ok := strings.CutPrefix(p.PlatformDigest, "sha256:"); ok && hex != "" {
			out = append(out, hex)
		}
	}
	return out
}
