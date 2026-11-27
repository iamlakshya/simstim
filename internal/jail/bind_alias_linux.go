package jail

import (
	"errors"
	"os"
	"path"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

func checkBackingBindAliases(binds []bind, files []*os.File) error {
	return checkBackingBindPairs(binds, files, readBackingMounts)
}

// Only a different-Write directory pair needs topology. Empty setups and
// compatible bindings have no new mountinfo dependency.
func checkBackingBindPairs(binds []bind, files []*os.File, load func() ([]backingMount, error)) error {
	defer runtime.KeepAlive(files)
	var mounts []backingMount
	loaded := false
	for i, parent := range binds {
		if parent.Kind != kindDir {
			continue
		}
		for j, child := range binds {
			// An anonymous supplied object has no host directory entry. Its
			// exact duplicate aliases are still checked by the ordinary native
			// identity checker; only pathname topology is inapplicable here.
			if child.AnonymousV1 {
				continue
			}
			if i == j || parent.Write == child.Write {
				continue
			}
			if len(files) < len(binds) {
				return failure("the jail's pinned bind descriptors are incomplete", "Report this as a bug.")
			}
			parentFD, err := backingBindFD(parent, files[i])
			if err != nil {
				return err
			}
			if _, err := backingBindFD(child, files[j]); err != nil {
				return err
			}
			if !loaded {
				mounts, err = load()
				if err != nil {
					return bindRelationshipError(parent, child, err)
				}
				loaded = true
			}
			if err := checkBackingBindPair(parent, parentFD, child, mounts); err != nil {
				return err
			}
		}
	}
	return nil
}

func backingBindFD(b bind, file *os.File) (int, error) {
	if file == nil {
		return -1, failure("the pinned bound path "+quote(b.Path)+" has no descriptor", "Keep bound paths unchanged during start.")
	}
	fd := int(file.Fd())
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return -1, pinError("cannot inspect the pinned bound path "+quote(b.Path), "Keep bound paths unchanged during start.", err)
	}
	if st.Dev != b.Dev || st.Ino != b.Ino || kindOf(st.Mode) != b.Kind {
		return -1, failure("the pinned bound path "+quote(b.Path)+" changed", "Keep bound paths unchanged during start.")
	}
	return fd, nil
}

func bindRelationshipError(parent, child bind, err error) error {
	return pinError("cannot verify the physical relationship of "+quote(parent.Path)+" and "+quote(child.Path),
		"Use binds with the same Write, or paths whose physical relationship can be verified.", err)
}

func checkBackingBindPair(parent bind, parentFD int, child bind, mounts []backingMount) error {
	relatives, err := backingBindRelatives(parent, child, mounts)
	if err != nil {
		return bindRelationshipError(parent, child, err)
	}
	var uncertain error
	for _, relative := range relatives {
		overlaps, err := backingBindCandidate(parentFD, relative, child)
		if overlaps {
			return failure("the bound path "+quote(child.Path)+" is inside "+quote(parent.Path)+", with other rights",
				"Bind the two paths with the same Write, or bind paths that do not overlap.")
		}
		// A failed or hidden candidate cannot discard a later positive
		// capability proof. Complete the candidate set before an error.
		if err != nil && uncertain == nil {
			uncertain = err
		}
	}
	if uncertain != nil {
		return bindRelationshipError(parent, child, uncertain)
	}
	return nil
}

// Filesystem paths on unlike devices are not comparable. Inversion through
// every child-device mount record also finds namespace names beneath a
// parent on another device, without guessing which stacked record is live.
func backingBindRelatives(parent, child bind, mounts []backingMount) ([]string, error) {
	parents, err := backingPaths(parent.Real, parent.Dev, mounts)
	if err != nil {
		return nil, err
	}
	children, err := backingPaths(child.Real, child.Dev, mounts)
	if err != nil {
		return nil, err
	}
	var relatives []string
	seen := make(map[string]bool)
	add := func(relative string) {
		if !seen[relative] {
			seen[relative] = true
			relatives = append(relatives, relative)
		}
	}
	for _, candidate := range children {
		if parent.Dev == child.Dev {
			for _, root := range parents {
				if relative, below := backingRelative(candidate, root); below {
					add(relative)
				}
			}
		}
		for _, mount := range mounts {
			if mount.major != uint64(unix.Major(child.Dev)) || mount.minor != uint64(unix.Minor(child.Dev)) {
				continue
			}
			relative, below := backingRelative(candidate, mount.root)
			if !below {
				continue
			}
			spelling := path.Join(mount.point, relative)
			if relative, below := backingRelative(spelling, parent.Real); below {
				add(relative)
			}
		}
	}
	return relatives, nil
}

func absentBindCandidate(err error) bool {
	return errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP)
}

func backingBindCandidate(parentFD int, relative string, child bind) (bool, error) {
	if path.IsAbs(relative) || path.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, "../") {
		return false, backingError("a possible bind overlap has no safe relative path")
	}
	fd, err := openBeneath(parentFD, "/", "/"+relative)
	if err == nil {
		matches, inspectErr := backingBindIdentity(fd, child)
		_ = unix.Close(fd)
		if matches || inspectErr != nil {
			return matches, inspectErr
		}
	} else if !absentBindCandidate(err) {
		return false, err
	}
	// A non-recursive bind drops host submounts. A current mismatch or
	// absence therefore disproves the proposed physical child only when
	// resolving the same name does not cross one of those submounts.
	fd, err = openat2(parentFD, relative, &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV})
	if errors.Is(err, unix.EXDEV) {
		return false, failure("a possible bind overlap is hidden by a child mount image",
			"Use the same Write for these binds, or avoid hidden parent mount images.")
	}
	if absentBindCandidate(err) {
		return false, nil
	}
	if errors.Is(err, unix.ENOSYS) {
		return false, errNoOpenat2
	}
	if err != nil {
		return false, pinError("cannot inspect a possible parent mount image", "Keep bound paths readable and stable during start.", err)
	}
	defer func() { _ = unix.Close(fd) }()
	return backingBindIdentity(fd, child)
}

func backingBindIdentity(fd int, child bind) (bool, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, stepFailed("cannot inspect a possible overlapping bind", "Keep bound paths unchanged during start.", err)
	}
	return st.Dev == child.Dev && st.Ino == child.Ino && kindOf(st.Mode) == child.Kind, nil
}
