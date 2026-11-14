//go:build !linux && !darwin

package files

import "os"

// PinMount refuses a platform without descriptor-relative mount validation.
func PinMount(string, bool) (*os.File, error) {
	return nil, mountError("bad_mount", "this host cannot pin a mount source safely")
}
