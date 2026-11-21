package host

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// This is the installs.json role of the existing outside metadata journal.
// Replay recognises the already-published original inode or cancels its exact
// unpublished part. It never publishes code, a file or a prepared metadata part.
func replayInstallArchiveV1(ctx context.Context, guard *os.Root, control *placementControl) error {
	intent, pending := control.ArchiveWrites["installs.json"]
	if !pending {
		return nil
	}
	if err := errors.Join(ctx.Err(), checkStateMetadataGuard(control, guard)); err != nil {
		return err
	}
	if !metadataWriteIntentKnown(intent) {
		return placementError("the installation archive intent has an unknown native schema")
	}
	current, err := metadataBefore(guard, "installs.json")
	if err != nil {
		return err
	}
	validate := func(file *os.File) error {
		return validateArchiveDescriptor(ctx, file, func(raw []byte) error { _, err := validateInstallLedgerV1(raw, control); return err })
	}
	if current != nil {
		file, err := cachefs.OpenFile(guard, "installs.json", os.O_RDONLY, 0o600)
		if err != nil {
			return err
		}
		err = validate(file)
		_ = file.Close()
		if err != nil {
			return err
		}
	}
	finish := func() error {
		delete(control.ArchiveWrites, "installs.json")
		return writePlacementControl(guard, *control)
	}
	if current != nil && intent.After.matches(current) {
		for _, name := range []string{intent.Temporary, intent.Quarantine} {
			if name == "" {
				continue
			}
			if _, err := guard.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				return placementError("preserved an unexpected temporary beside the published installation archive")
			}
		}
		if control.Archives == nil {
			control.Archives = map[string]purgeRef{}
		}
		control.Archives["installs.json"] = refOf(current)
		return finish()
	}
	before := intent.Before == nil && current == nil || intent.Before != nil && current != nil && intent.Before.matches(current)
	if !before || current != nil && !control.Archives["installs.json"].matches(current) {
		return placementError("preserved an installation archive that matches neither original generation")
	}
	if intent.Quarantine != "" {
		if _, err := guard.Lstat(intent.Quarantine); err == nil {
			if _, err := guard.Lstat(intent.Temporary); !errors.Is(err, os.ErrNotExist) {
				return placementError("preserved both installation archive part and cleanup capture")
			}
			file, err := cachefs.OpenFile(guard, intent.Quarantine, os.O_RDONLY, 0o600)
			if err != nil {
				return err
			}
			defer file.Close()
			info, err := cachefs.ValidateFile(guard, intent.Quarantine, file, 0o600)
			if err != nil || !intent.After.matches(info) {
				return placementError("the installation archive capture changed its original inode")
			}
			if err := validate(file); err != nil {
				return err
			}
			info, err = cachefs.ValidateFile(guard, intent.Quarantine, file, 0o600)
			if err != nil || !intent.After.matches(info) {
				return placementError("the captured installation archive changed before unlink")
			}
			if err := guard.Remove(intent.Quarantine); err != nil {
				return err
			}
			if err := syncCapturedDirectory(guard); err != nil {
				return err
			}
			return finish()
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if _, err := guard.Lstat(intent.Temporary); errors.Is(err, os.ErrNotExist) {
		return finish()
	} else if err != nil {
		return err
	}
	capture := intent.Quarantine
	if capture == "" {
		nonce, err := footprintNonce()
		if err != nil {
			return err
		}
		capture = ".metadata-cleanup-" + nonce
	}
	if err := removeCapturedFile(guard, intent.Temporary, intent.After, capture, func(name string) error {
		intent.Quarantine = name
		control.ArchiveWrites["installs.json"] = intent
		return writePlacementControl(guard, *control)
	}, validate); err != nil {
		return err
	}
	return finish()
}
