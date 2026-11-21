//go:build linux

package host

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// This is ONLY a bounded public-DATA restriction at the Root-adopted operator
// boundary. It never attests current peer mm, constructs an OP or lends any
// private descriptor. Both original and candidate are actual retained files.
func checkPurgePublicCodeBytesV3(ctx context.Context, original, candidate *os.File, originalInfo os.FileInfo) error {
	if ctx == nil || original == nil || candidate == nil || originalInfo == nil {
		return placementError("the purge DATA code check has no original retained native files")
	}
	flags, err := unix.FcntlInt(candidate.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_PATH != 0 || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return errors.Join(placementError("the purge DATA code role is not an actual readable-only descriptor"), err)
	}
	before, err := candidate.Stat()
	if err != nil {
		return err
	}
	_, _, uid, links, known := footprintIdentity(before)
	if !known || uid != uint64(os.Getuid()) || links > 1 || !before.Mode().IsRegular() || before.Mode().Perm()&0o022 != 0 || before.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return placementError("the purge DATA code role has unknown native ownership or aliases")
	}
	if err := verifyOriginalPurgeCodeBytesV2(ctx, original, candidate, originalInfo); err != nil {
		return err
	}
	after, err := candidate.Stat()
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return errors.Join(placementError("the purge DATA code descriptor changed during its exact byte comparison"), err)
	}
	return ctx.Err()
}

func (image *purgeAttemptImageV2) checkRelayPublicCodeV3(ctx context.Context, candidate *os.File) error {
	if image == nil {
		return placementError("the purge DATA receiver has no own current protected image")
	}
	image.mu.Lock()
	defer image.mu.Unlock()
	if err := image.checkLockedV2(ctx); err != nil {
		return err
	}
	return checkPurgePublicCodeBytesV3(ctx, image.original, candidate, image.originalInfo)
}
