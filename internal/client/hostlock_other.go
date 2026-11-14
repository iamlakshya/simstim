//go:build !unix && !windows

package client

import "os"

func lockExclusive(*os.File) error { return lockUnsupported() }
