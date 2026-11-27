//go:build linux

package host

import "os"

// Read-only observation of SAME original descriptor objects. This cannot
// release an unlinked artefact or create a source from missing named data.
// Callers use it only after their own physical cleanup reached final Close.
func observeOriginalPurgeDescriptorCloseV3(files []*os.File, roots []*os.Root) error {
	return (&purgeCloseStepV3{files: files, roots: roots}).checkClosedV3()
}
