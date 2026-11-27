package host

import (
	"context"
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// A pre-existing link with no native producer receipt stays untouched. The
// client selects its exact version path and does not need to adopt that link.
func installCurrentLinkV1(ctx context.Context, publisher *stateMetadataPublisher, version string) error {
	root, err := cachefs.OpenPrivateRoot(paths.BinDir(), false)
	if err != nil {
		return err
	}
	defer root.Close()
	return withInstallerControlV1(publisher, func(control *placementControl, guard *os.Root) error {
		ledger, err := readInstallLedgerV1(ctx, guard, control)
		if err != nil {
			return err
		}
		if err := replayInstallCurrentV1(ctx, root, guard, control, &ledger); err != nil {
			return err
		}
		if current, err := root.Lstat("current"); err == nil {
			if ledger.Current == nil || !ledger.Current.Ref.matches(current) {
				return nil
			}
			target, err := root.Readlink("current")
			if err != nil || target != ledger.Current.Target {
				return placementError("the current helper link changed its original target")
			}
			if target == version {
				return nil
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		} else if ledger.Current != nil {
			return placementError("the original current helper selector is missing without its removal receipt")
		}
		nonce, err := footprintNonce()
		if err != nil {
			return err
		}
		temporary := ".current-" + nonce
		if err := root.Symlink(version, temporary); err != nil {
			return err
		}
		info, err := root.Lstat(temporary)
		if err != nil {
			return err
		}
		ref := refOf(info)
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return placementError("the prepared current link has another native type")
		}
		after := installLinkV1{Ref: ref, Target: version}
		ledger.PendingCurrent = &installCurrentIntentV1{Before: ledger.Current, After: after, Prepared: temporary, Capture: ".current-old-" + nonce}
		// The original native reference is durable before publication. A
		// failed publication preserves its prepared link for exact replay.
		if err := writeInstallLedgerV1(ctx, guard, control, ledger); err != nil {
			return err
		}
		if ledger.Current != nil {
			if err := checkInstallLinkV1(root, "current", *ledger.Current); err != nil {
				return err
			}
			if err := cachefs.RenameNoReplace(root, "current", ledger.PendingCurrent.Capture); err != nil {
				return err
			}
			if err := checkInstallLinkV1(root, ledger.PendingCurrent.Capture, *ledger.Current); err != nil {
				return err
			}
		}
		if err := cachefs.RenameNoReplace(root, temporary, "current"); err != nil {
			return err
		}
		current, err := root.Lstat("current")
		if err != nil || !ref.matches(current) {
			return placementError("the prepared current link changed during publication")
		}
		if err := syncCapturedDirectory(root); err != nil {
			return err
		}
		return replayInstallCurrentV1(ctx, root, guard, control, &ledger)
	})
}

func validInstallLinkV1(link installLinkV1) bool {
	return validInstallVersionV1(link.Target) && len(link.Ref.extra) == 0 && link.Ref.Inode != 0 && link.Ref.UID == uint64(os.Getuid()) && os.FileMode(link.Ref.Mode).Type() == os.ModeSymlink && !link.Ref.ModTime.IsZero()
}

func checkInstallLinkV1(root *os.Root, name string, link installLinkV1) error {
	info, err := root.Lstat(name)
	if err != nil || !validInstallLinkV1(link) || !link.Ref.matches(info) {
		return placementError("preserved a helper link with another native identity")
	}
	target, err := root.Readlink(name)
	if err != nil || target != link.Target {
		return placementError("preserved a helper link with another target")
	}
	return nil
}

// Replay reconciles an actually published selector, or cancels the original
// unpublished part and restores its exact old link without replacement. It
// never publishes a prepared selector after its producer returned.
func replayInstallCurrentV1(ctx context.Context, root, guard *os.Root, control *placementControl, ledger *installLedgerV1) error {
	pending := ledger.PendingCurrent
	if pending == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, currentErr := root.Lstat("current")
	published := currentErr == nil && pending.After.Ref.matches(current)
	if currentErr != nil && !errors.Is(currentErr, os.ErrNotExist) {
		return currentErr
	}
	removeExact := func(name string, link installLinkV1) error {
		if _, err := root.Lstat(name); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if err := checkInstallLinkV1(root, name, link); err != nil {
			return err
		}
		return root.Remove(name)
	}
	if published {
		if err := checkInstallLinkV1(root, "current", pending.After); err != nil {
			return err
		}
		if _, err := root.Lstat(pending.Prepared); !errors.Is(err, os.ErrNotExist) {
			return placementError("preserved a second prepared selector beside the published link")
		}
		if pending.Before != nil {
			if err := removeExact(pending.Capture, *pending.Before); err != nil {
				return err
			}
		}
		copy := pending.After
		ledger.Current = &copy
	} else {
		if err := removeExact(pending.Prepared, pending.After); err != nil {
			return err
		}
		if currentErr == nil {
			if pending.Before == nil || !pending.Before.Ref.matches(current) {
				return placementError("preserved a replacement helper selector during installation recovery")
			}
			if err := checkInstallLinkV1(root, "current", *pending.Before); err != nil {
				return err
			}
		} else if pending.Before != nil {
			if err := checkInstallLinkV1(root, pending.Capture, *pending.Before); err != nil {
				return err
			}
			if err := cachefs.RenameNoReplace(root, pending.Capture, "current"); err != nil {
				return err
			}
		}
		ledger.Current = pending.Before
	}
	if err := syncCapturedDirectory(root); err != nil {
		return err
	}
	ledger.PendingCurrent = nil
	return writeInstallLedgerV1(ctx, guard, control, *ledger)
}
