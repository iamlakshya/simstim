//go:build !linux

package host

import (
	"context"

	"github.com/iamlakshya/simstim/internal/api"
)

func readPlatformCapabilities(_ context.Context, _ capSource, _ *api.CapProbe) {}
func trustedRootFile(string) bool                                              { return false }
func probeDisk(dataDir, _ string) (int64, string, bool)                        { return diskFree(dataDir), "", false }
