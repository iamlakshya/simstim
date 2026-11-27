//go:build linux

package process

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/wall"
)

// copyPlacementV3 admits persistent staging only under the globally protected
// session store. User grants already exclude that store and all its ancestors.
// The actual factory also excludes foreign private subtrees and any source
// that reaches its own session root. Kept work/state are sibling subtrees.
func copyPlacementV3(session string, plan []wall.Mount) (bool, error) {
	protected, err := filepath.EvalSymlinks(paths.StateDir())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	sessions, err := filepath.EvalSymlinks(paths.SessionsDir())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(sessions, session)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return false, nil
	}
	private, err := pinSource(session)
	if err != nil {
		return false, err
	}
	defer private.Close()
	for _, mount := range plan {
		source, err := pinSource(mount.Host)
		if err != nil {
			return false, err
		}
		contains, inspectErr := copyContainsV3(source, private)
		closeErr := source.Close()
		if inspectErr != nil || closeErr != nil {
			return false, errors.Join(inspectErr, closeErr)
		}
		if contains {
			return false, fmt.Errorf("a guest source reaches the original private session store")
		}
		// A constructor-owned work/state child may be exposed; another session's
		// store and its ancestors remain unavailable even through direct drivers.
		resolved, err := filepath.EvalSymlinks(mount.Host)
		if err != nil {
			return false, err
		}
		inside := resolved == protected || strings.HasPrefix(resolved, protected+string(os.PathSeparator))
		own := strings.HasPrefix(resolved, session+string(os.PathSeparator))
		if inside && !own {
			return false, fmt.Errorf("a guest source reaches another private session store")
		}
	}
	return true, nil
}

// copyContainsV3 compares captured directory identities while walking actual
// parent handles. Device equality alone never grants containment or staging.
func copyContainsV3(source, descendant *os.File) (bool, error) {
	wanted, err := source.Stat()
	if err != nil {
		return false, err
	}
	if !wanted.IsDir() {
		return false, nil
	}
	currentFD, err := unix.Openat(int(descendant.Fd()), ".", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	current := os.NewFile(uintptr(currentFD), "private-copy-ancestor")
	for {
		info, err := current.Stat()
		if err != nil {
			return false, errors.Join(err, current.Close())
		}
		if os.SameFile(wanted, info) {
			return true, current.Close()
		}
		parentFD, err := unix.Openat(int(current.Fd()), "..", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return false, errors.Join(err, current.Close())
		}
		parent := os.NewFile(uintptr(parentFD), "private-copy-ancestor")
		parentInfo, err := parent.Stat()
		closeErr := current.Close()
		if err != nil || closeErr != nil {
			return false, errors.Join(err, closeErr, parent.Close())
		}
		if os.SameFile(info, parentInfo) {
			return false, parent.Close()
		}
		current = parent
	}
}

func copyMountIDV3(file *os.File) (uint64, error) {
	var info unix.Statx_t
	if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_NO_AUTOMOUNT, unix.STATX_MNT_ID, &info); err != nil {
		return 0, err
	}
	if info.Mask&unix.STATX_MNT_ID == 0 {
		return 0, fmt.Errorf("the original mount identity is unavailable")
	}
	return info.Mnt_id, nil
}

// prepareCopyMountV3 creates staging through the retained protected storage
// root before any child exists. The source and staging must name the same
// actual original mount, so atomic rename never crosses a guest bind/overlay.
func prepareCopyMountV3(storage *os.Root, source *os.File) (*copyMountNativeV1, *os.File, error) {
	parent, err := storage.Open(".")
	if err != nil {
		return nil, nil, err
	}
	sourceID, sourceErr := copyMountIDV3(source)
	parentID, parentErr := copyMountIDV3(parent)
	if sourceErr != nil || parentErr != nil {
		return nil, nil, errors.Join(sourceErr, parentErr, parent.Close())
	}
	if sourceID != parentID {
		return nil, nil, errors.Join(unix.EXDEV, parent.Close())
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, nil, errors.Join(err, parent.Close())
	}
	name := ".copy-" + hex.EncodeToString(random[:])
	rootFD, stageFD, extraFD := -1, -1, -1
	rootFD, err = unix.FcntlInt(source.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, nil, errors.Join(err, parent.Close())
	}
	root := os.NewFile(uintptr(rootFD), "original-copy-mount")
	if err = unix.Mkdirat(int(parent.Fd()), name, 0700); err != nil {
		return nil, nil, errors.Join(err, root.Close(), parent.Close())
	}
	stageFD, err = unix.Openat(int(parent.Fd()), name, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, errors.Join(err, unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR), root.Close(), parent.Close())
	}
	stage := os.NewFile(uintptr(stageFD), "original-private-copy-stage")
	identity, err := stage.Stat()
	if err != nil {
		return nil, nil, errors.Join(err, unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR), stage.Close(), root.Close(), parent.Close())
	}
	mount := &copyMountNativeV1{root: root, stage: stage, cleanupRoot: parent, ownedName: name, identity: identity}
	extraFD, err = unix.FcntlInt(stage.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, nil, errors.Join(err, copyUnlinkOwned(parent, name, identity, true), stage.Close(), root.Close(), parent.Close())
	}
	return mount, os.NewFile(uintptr(extraFD), "launcher-private-copy-stage"), nil
}
