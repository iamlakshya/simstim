// Package runtimecache stores runtime files authorised by a client's release
// manifest. It uses supply's canonical validation and byte checks.
package runtimecache

import (
	"fmt"
	"io/fs"
	"math"
	"regexp"
	"slices"

	"github.com/iamlakshya/simstim/internal/supply"
)

// Resource bounds apply before a transfer allocates storage or starts I/O.
const (
	ChunkSize    = supply.TransferChunkSize
	MaxTransfers = 4
)

var labelForm = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// ValidLabel reports whether label names one bounded runtime record.
func ValidLabel(label string) bool { return labelForm.MatchString(label) }

// IsRuntime reports whether kind belongs in the unprivileged runtime store.
// Image builders and privileged launchers have separate installation paths.
func IsRuntime(kind string) bool {
	switch kind {
	case supply.KindUserns, supply.KindVMM, supply.KindKernel, supply.KindTools, supply.KindGvisor:
		return true
	default:
		return false
	}
}

// Mode is the immutable mode fixed by an authorised runtime role.
func Mode(item supply.Item) fs.FileMode {
	if item.Kind == supply.KindVMM || item.Kind == supply.KindGvisor {
		return 0o555
	}
	return 0o444
}

// Validate checks a bounded runtime-only manifest for one real architecture.
func Validate(m supply.Manifest, arch string) error { return ValidateWithImageDefaults(m, nil, arch) }

// ValidateWithImageDefaults uses the canonical supply validator for the
// combined client subsets, then enforces their separate role boundaries.
// Nil defaults mean unknown legacy metadata. An explicit empty array is known.
func ValidateWithImageDefaults(m supply.Manifest, defaults *[]supply.Item, arch string) error {
	if !ValidLabel(m.Runtime) {
		return fmt.Errorf("runtime label must be one name of at most 64 characters")
	}
	if len(m.Items) == 0 {
		return fmt.Errorf("runtime manifest needs at least one item")
	}
	if defaults != nil && *defaults == nil {
		return fmt.Errorf("image defaults must be an explicit array")
	}
	combined := m
	combined.Items = slices.Clone(m.Items)
	if defaults != nil {
		combined.Items = append(combined.Items, (*defaults)...)
	}
	if err := combined.Validate(); err != nil {
		return fmt.Errorf("validate runtime manifest: %w", err)
	}
	seen := map[string]supply.Item{}
	for _, it := range m.Items {
		if !IsRuntime(it.Kind) {
			return fmt.Errorf("role %q has no runtime delivery path", it.Kind)
		}
		if it.Size == math.MaxInt64 {
			return fmt.Errorf("runtime size cannot be read with its required excess-byte check")
		}
		if it.Arch != arch {
			return fmt.Errorf("runtime architecture %q does not match %q", it.Arch, arch)
		}
		if old, ok := seen[it.SHA256]; ok && (old.Size != it.Size || Mode(old) != Mode(it)) {
			return fmt.Errorf("runtime digest has conflicting sizes or modes")
		}
		seen[it.SHA256] = it
	}
	if defaults != nil {
		for _, item := range *defaults {
			if item.Kind != supply.KindImage {
				return fmt.Errorf("image defaults contain a non-image role")
			}
			if item.Arch != arch {
				return fmt.Errorf("image default architecture does not match this host")
			}
		}
	}
	return nil
}
