//go:build linux

package host

import (
	"errors"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// custodyRetiredConfigV1 owns only an actual rewrite's original descriptor.
// A failed reload cannot drop that cleanup owner or select a substitute name.
type custodyRetiredConfigV1 struct {
	file       *custodyConfigFileV1
	removed    bool
	closeFacts *supply.FileCloseFactsV1
}

func (retired *custodyRetiredConfigV1) closeV1() error {
	if retired == nil || retired.file == nil || retired.file.file == nil {
		return nil
	}
	file := retired.file
	if !retired.removed {
		var stat unix.Stat_t
		if err := unix.Fstat(int(file.file.Fd()), &stat); err != nil {
			return err
		}
		if stat.Nlink == 0 {
			// The SAME retained inode is already unlinked. Preserve anything
			// newly bound at its old name and close only this real descriptor.
			retired.removed = true
		} else {
			var err error
			if file.incomplete {
				err = file.removeCreatedV1()
			} else {
				err = file.remove()
			}
			if err != nil {
				return err
			}
			retired.removed = true
		}
	}
	if err := syncCapturedDirectory(file.root); err != nil {
		return err
	}
	if retired.closeFacts == nil {
		facts := supply.CloseFileOnceV1(file.file)
		retired.closeFacts = &facts
	}
	if !retired.closeFacts.Retired || retired.closeFacts.Unknown != nil {
		return errors.Join(sessionLeaseError("the original retired configuration descriptor has no confirmed native retirement"), retired.closeFacts.Unknown)
	}
	file.file = nil
	return retired.closeFacts.Diagnostic
}

// Non-destructive descriptor retirement for a still-named current config.
// Unlike retired-file cleanup this never removes or selects a pathname.
func (file *custodyConfigFileV1) closeDescriptorV1() error {
	if file == nil || file.file == nil {
		return nil
	}
	if file.closeFacts == nil {
		facts := supply.CloseFileOnceV1(file.file)
		file.closeFacts = &facts
	}
	if !file.closeFacts.Retired || file.closeFacts.Unknown != nil {
		return errors.Join(sessionLeaseError("the original runtime descriptor has no confirmed native retirement"), file.closeFacts.Unknown)
	}
	file.file = nil
	return file.closeFacts.Diagnostic
}
