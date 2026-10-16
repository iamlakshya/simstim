//go:build !unix

package diag

import "os"

// markInUse does nothing here: the system does not remove an open file.
func markInUse(*os.File) {}

// inUse is false here: os.Remove fails on an open file by itself.
func inUse(string) bool { return false }
