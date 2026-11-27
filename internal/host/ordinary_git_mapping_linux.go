//go:build linux

package host

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

// A mapping pins regular control files and an administrative directory opened
// beneath the retained original common root. Its observations grant no ref,
// administrative record or worktree deletion rights.
type ordinaryGitMappingV2 struct {
	work, common *os.Root
	admin        *os.Root
	adminRef     purgeRef
	relative     string
	files        []*ordinaryGitControlFileV2
}

type ordinaryGitControlFileV2 struct {
	root *os.Root
	leaf string
	file *os.File
	info os.FileInfo
	raw  []byte
}

func captureOrdinaryGitMappingV2(work, common *os.Root, origin worktreeOriginV1) (_ *ordinaryGitMappingV2, returned error) {
	if work == nil || common == nil || origin.Directory.matchesRoot(work) != nil || origin.CommonRef.matchesRoot(common) != nil {
		return nil, sessionLeaseError("the original Git mapping has no retained worktree and common roots")
	}
	mapping := &ordinaryGitMappingV2{work: work, common: common}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, mapping.Close())
		}
	}()
	gitfile, err := captureOrdinaryGitControlFileV2(work, ".git")
	if err != nil {
		return nil, err
	}
	mapping.files = append(mapping.files, gitfile)
	text := string(gitfile.raw)
	if !strings.HasPrefix(text, "gitdir: ") || !strings.HasSuffix(text, "\n") || strings.Count(text, "\n") != 1 || strings.ContainsAny(text, "\r\x00") {
		return nil, sessionLeaseError("the original worktree has no exact regular Git administrative mapping")
	}
	path := strings.TrimSuffix(strings.TrimPrefix(text, "gitdir: "), "\n")
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, sessionLeaseError("the worktree's Git administrative mapping has no exact absolute target")
	}
	relative, err := filepath.Rel(origin.Common, path)
	parts := strings.Split(relative, string(filepath.Separator))
	if err != nil || !validPurgeRelative([]byte(relative)) || len(parts) != 2 || parts[0] != "worktrees" || parts[1] == "." || parts[1] == ".." {
		return nil, sessionLeaseError("the worktree's administrative mapping leaves its original common root")
	}
	mapping.relative = relative
	mapping.admin, mapping.adminRef, err = openOrdinaryGitAdminV2(common, relative)
	if err != nil {
		return nil, err
	}
	for _, expected := range []struct{ leaf, text string }{
		{"HEAD", "ref: refs/heads/" + origin.Branch + "\n"},
		{"gitdir", work.Name() + string(filepath.Separator) + ".git\n"},
	} {
		control, err := captureOrdinaryGitControlFileV2(mapping.admin, expected.leaf)
		if err != nil {
			return nil, err
		}
		mapping.files = append(mapping.files, control)
		if string(control.raw) != expected.text {
			return nil, sessionLeaseError("the Git administrative directory does not bind the original branch and worktree")
		}
	}
	if err := mapping.Check(); err != nil {
		return nil, err
	}
	complete = true
	return mapping, nil
}

func captureOrdinaryGitControlFileV2(root *os.Root, leaf string) (_ *ordinaryGitControlFileV2, returned error) {
	info, err := root.Lstat(leaf)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() < 1 || info.Size() > 4096 {
		return nil, sessionLeaseError("the original Git control file has an unsafe native type, mode or bound")
	}
	file, err := cachefs.OpenOwnedFileExisting(root, leaf, info.Mode().Perm())
	if err != nil {
		return nil, err
	}
	control := &ordinaryGitControlFileV2{root: root, leaf: leaf, file: file, info: info}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, file.Close())
		}
	}()
	control.raw, err = io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(control.raw) > 4096 {
		return nil, errors.Join(sessionLeaseError("the original Git control file changed its read bound"), err)
	}
	if err := control.Check(); err != nil {
		return nil, err
	}
	complete = true
	return control, nil
}

func (control *ordinaryGitControlFileV2) Check() error {
	if control == nil || control.root == nil || control.file == nil || control.info == nil {
		return sessionLeaseError("the original Git control descriptor is absent")
	}
	current, err := cachefs.ValidateOwnedFile(control.root, control.leaf, control.file, control.info.Mode().Perm())
	if err != nil || !os.SameFile(current, control.info) || current.Size() != control.info.Size() || !current.ModTime().Equal(control.info.ModTime()) {
		return errors.Join(sessionLeaseError("the original Git control file changed its native binding"), err)
	}
	raw := make([]byte, len(control.raw)+1)
	n, err := control.file.ReadAt(raw, 0)
	if err != nil && !errors.Is(err, io.EOF) || !bytes.Equal(raw[:n], control.raw) {
		return errors.Join(sessionLeaseError("the original Git control file changed its exact mapping contents"), err)
	}
	return nil
}

// The one intentional procfd view is created from an openat2 result beneath
// the original common root. No mutable absolute path chooses the admin root.
func openOrdinaryGitAdminV2(common *os.Root, relative string) (_ *os.Root, _ purgeRef, returned error) {
	parts := strings.Split(relative, string(filepath.Separator))
	if common == nil || !validPurgeRelative([]byte(relative)) || len(parts) != 2 || parts[0] != "worktrees" {
		return nil, purgeRef{}, sessionLeaseError("the original Git administrative root has another fixed role")
	}
	directory, err := common.Open(".")
	if err != nil {
		return nil, purgeRef{}, err
	}
	defer func() { returned = errors.Join(returned, directory.Close()) }()
	fd, err := unix.Openat2(int(directory.Fd()), relative, &unix.OpenHow{Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, purgeRef{}, err
	}
	file := os.NewFile(uintptr(fd), "original-Git-administrative-directory")
	defer func() { returned = errors.Join(returned, file.Close()) }()
	info, err := file.Stat()
	if err != nil || !info.IsDir() || refOf(info).UID != uint64(os.Getuid()) || info.Mode().Perm()&0o022 != 0 {
		return nil, purgeRef{}, errors.Join(sessionLeaseError("the original Git administrative directory has unsafe native ownership"), err)
	}
	root, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", file.Fd()))
	if err != nil {
		return nil, purgeRef{}, err
	}
	ref := refOf(info)
	if err := ref.matchesRoot(root); err != nil {
		return nil, purgeRef{}, errors.Join(err, root.Close())
	}
	return root, ref, nil
}

func (mapping *ordinaryGitMappingV2) Check() error {
	if mapping == nil || mapping.work == nil || mapping.common == nil || mapping.admin == nil || len(mapping.files) != 3 {
		return sessionLeaseError("the original Git mapping is incomplete")
	}
	if err := mapping.adminRef.matchesRoot(mapping.admin); err != nil {
		return err
	}
	fresh, ref, err := openOrdinaryGitAdminV2(mapping.common, mapping.relative)
	if err != nil {
		return err
	}
	if err := errors.Join(mapping.adminRef.matchesRoot(fresh), fresh.Close()); err != nil {
		return err
	}
	if !samePurgeDirectoryRef(mapping.adminRef, ref) {
		return sessionLeaseError("the original common root names a replaced Git administrative directory")
	}
	for _, control := range mapping.files {
		if err := control.Check(); err != nil {
			return err
		}
	}
	return nil
}

func (mapping *ordinaryGitMappingV2) Close() error {
	if mapping == nil {
		return nil
	}
	var failure error
	for _, control := range mapping.files {
		failure = errors.Join(failure, control.file.Close())
	}
	if mapping.admin != nil {
		failure = errors.Join(failure, mapping.admin.Close())
	}
	mapping.files, mapping.admin = nil, nil
	return failure
}
