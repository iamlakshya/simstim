//go:build !linux

package fsbirth

import "os"

// Capture produces no native generation for an unsupported platform.
func Capture(*os.Root) (*DataV2, error) { return nil, nil }
