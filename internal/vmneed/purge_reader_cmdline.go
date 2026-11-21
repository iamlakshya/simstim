package vmneed

import (
	"fmt"
	"strings"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

// PurgeReaderKernelCommandLineV1 only routes the original verified tools/image
// tables into the different closed reader entry. A host must already own its
// actual distinct copy-boot issuer; this text creates no source or permission.
func PurgeReaderKernelCommandLineV1(tools supply.Item, root image.Request) (string, error) {
	ordinary := KernelCommandLine(tools, root)
	const normal = " -- wall-init --microvm-v1"
	if !strings.HasSuffix(ordinary, normal) {
		return "", fmt.Errorf("the canonical VM init argument no longer matches the fixed reader ABI")
	}
	return strings.TrimSuffix(ordinary, normal) + " -- wall-init --purge-reader-v1", nil
}
