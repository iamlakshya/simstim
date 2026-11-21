package host

import (
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
)

func (owner *sandboxPrepareCleanupV1) removeTerminalV1(name string, original *os.File, reference cachefs.Reference, claimed *bool, claim *string) error {
	root := owner.reservation.dir
	if err := owner.check(); err != nil {
		return err
	}
	if !*claimed {
		if err := reference.CheckFile(root, name, original, 0o600); err != nil {
			return err
		}
		nonce, err := api.RandomSessionID()
		if err != nil {
			return err
		}
		selected := ".prepare-terminal-" + nonce
		if err := cachefs.RenameNoReplace(root, name, selected); err != nil {
			return err
		}
		captured, err := root.Lstat(selected)
		native, nativeErr := original.Stat()
		if err != nil || nativeErr != nil || !os.SameFile(captured, native) {
			return errors.Join(sessionLeaseError("prepare cleanup preserved a replaced terminal native claim"), err, nativeErr,
				cachefs.RenameNoReplace(root, selected, name))
		}
		*claim, *claimed = selected, true
	}
	if *claim == "" {
		return nil
	}
	captured, err := root.Lstat(*claim)
	native, nativeErr := original.Stat()
	if err != nil || nativeErr != nil || !os.SameFile(captured, native) {
		return errors.Join(sessionLeaseError("the original terminal prepare native claim changed"), err, nativeErr)
	}
	if err := owner.check(); err != nil {
		return err
	}
	if err := root.Remove(*claim); err != nil {
		return err
	}
	*claim = ""
	return nil
}
