//go:build !linux

package supervisor

import "os"

func cloneForkFileV1(destination, source *os.File) (bool, error) { return false, nil }
