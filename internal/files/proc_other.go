//go:build !linux

package files

import "os"

// IsProcDir reports false on hosts without the Linux procfs magic links.
func IsProcDir(_ *os.File) (bool, error) { return false, nil }
