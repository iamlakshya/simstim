package host

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/supply"
)

// A pin restricts this actual removal to the opened native namespace. It does
// not adopt its children or claim the directory was created by the tool.
type purgeRootPinV1 struct {
	root *os.Root
	ref  purgeRef
}

func (owner *purgeOwnerV1) pinRoots() error {
	owner.roots = map[string]*purgeRootPinV1{}
	return owner.gate(func(control *placementControl, _ *os.Root) error {
		for _, role := range []string{"state", "data", "config"} {
			path, err := purgeRootPath(control, role)
			if err != nil {
				return err
			}
			root, err := cachefs.OpenPrivateRoot(path, false)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				owner.reportItem(false, "directory", role, "its original namespace could not be safely pinned: "+err.Error())
				return err
			}
			directory, err := root.Open(".")
			if err != nil {
				root.Close()
				return err
			}
			info, err := directory.Stat()
			directory.Close()
			if err != nil {
				root.Close()
				return err
			}
			owner.roots[role] = &purgeRootPinV1{root: root, ref: refOf(info)}
		}
		return nil
	})
}

func (owner *purgeOwnerV1) checkRoot(role string, control *placementControl) (*os.Root, error) {
	pin := owner.roots[role]
	if pin == nil {
		return nil, placementError("the removal has no original opened namespace for this role")
	}
	path, err := purgeRootPath(control, role)
	if err != nil {
		return nil, err
	}
	current, err := cachefs.OpenPrivateRoot(path, false)
	if err != nil {
		return nil, err
	}
	defer current.Close()
	if err := pin.ref.matchesRoot(current); err != nil {
		return nil, err
	}
	if err := pin.ref.matchesRoot(pin.root); err != nil {
		return nil, err
	}
	return pin.root, nil
}

// The real cache/file producer supplies ref and validation. The durable
// outside intent survives state/data deletion and never nominates another root.
func (owner *purgeOwnerV1) removeFile(role, relative string, ref purgeRef, validate func(*os.File) error) error {
	if !validPurgeRelative([]byte(relative)) {
		return placementError("the producer supplied an invalid storage-relative resource")
	}
	return owner.gate(func(control *placementControl, guard *os.Root) error {
		root, err := owner.checkRoot(role, control)
		if err != nil {
			return err
		}
		parent := root
		if directory := filepath.Dir(relative); directory != "." {
			parent, err = cachefs.OpenSafeAncestor(filepath.Join(root.Name(), directory))
			if err != nil {
				return err
			}
			defer parent.Close()
		}
		leaf := filepath.Base(relative)
		if control.Pending != nil {
			return placementError("another original captured resource awaits exact replay")
		}
		nonce, err := footprintNonce()
		if err != nil {
			return err
		}
		directory, err := parent.Open(".")
		if err != nil {
			return err
		}
		info, err := directory.Stat()
		_ = directory.Close()
		if err != nil {
			return err
		}
		file, err := cachefs.OpenOwnedFileExisting(parent, leaf, os.FileMode(ref.Mode).Perm())
		if err != nil {
			return err
		}
		digest, err := purgeFileDigestV1(owner.ctx, file)
		if err == nil && validate != nil {
			_, err = file.Seek(0, io.SeekStart)
			if err == nil {
				err = validate(file)
			}
		}
		current, checked := cachefs.ValidateOwnedFile(parent, leaf, file, os.FileMode(ref.Mode).Perm())
		_ = file.Close()
		if err != nil {
			return err
		}
		if checked != nil || !ref.matches(current) {
			return placementError("the original resource changed before its removal intent")
		}
		resource := &purgeResource{Root: role, Relative: []byte(relative), Ref: ref, Directory: refOf(info), SHA256: digest, Quarantine: ".purge-" + nonce}
		control.Pending = resource
		if err := writePlacementControl(guard, *control); err != nil {
			return err
		}
		if err := removeCapturedOwnedFileV1(parent, leaf, ref, resource.Quarantine, func(string) error {
			if err := owner.ctx.Err(); err != nil {
				return err
			}
			_, err := owner.checkRoot(role, control)
			return err
		}, func(file *os.File) error { return checkPurgeResourceFileV1(owner.ctx, resource, file) }); err != nil {
			return err
		}
		control.Pending = nil
		return writePlacementControl(guard, *control)
	})
}

func (owner *purgeOwnerV1) replayResource(ctx context.Context) error {
	return owner.gate(func(control *placementControl, guard *os.Root) error {
		resource := control.Pending
		if resource == nil {
			return nil
		}
		root, err := owner.checkRoot(resource.Root, control)
		if err != nil {
			return err
		}
		parent := root
		if directory := filepath.Dir(string(resource.Relative)); directory != "." {
			parent, err = cachefs.OpenSafeAncestor(filepath.Join(root.Name(), directory))
			if err != nil {
				return err
			}
			defer parent.Close()
		}
		if err := resource.Directory.matchesRoot(parent); err != nil {
			return err
		}
		leaf := filepath.Base(string(resource.Relative))
		_, captureErr := parent.Lstat(resource.Quarantine)
		_, currentErr := parent.Lstat(leaf)
		switch {
		case captureErr == nil && errors.Is(currentErr, os.ErrNotExist):
			file, err := cachefs.OpenOwnedFileExisting(parent, resource.Quarantine, os.FileMode(resource.Ref.Mode).Perm())
			if err != nil {
				return err
			}
			defer file.Close()
			info, err := cachefs.ValidateOwnedFile(parent, resource.Quarantine, file, os.FileMode(resource.Ref.Mode).Perm())
			if err != nil || !resource.Ref.matches(info) {
				return placementError("the original resource capture changed its native identity")
			}
			if err := checkPurgeResourceFileV1(ctx, resource, file); err != nil {
				return err
			}
			if err := resource.Directory.matchesRoot(parent); err != nil {
				return err
			}
			if err := parent.Remove(resource.Quarantine); err != nil {
				return err
			}
			if err := syncCapturedDirectory(parent); err != nil {
				return err
			}
		case errors.Is(captureErr, os.ErrNotExist) && currentErr == nil:
			if err := removeCapturedOwnedFileV1(parent, leaf, resource.Ref, resource.Quarantine, func(string) error { return errors.Join(ctx.Err(), resource.Directory.matchesRoot(parent)) }, func(file *os.File) error { return checkPurgeResourceFileV1(ctx, resource, file) }); err != nil {
				return err
			}
		case errors.Is(captureErr, os.ErrNotExist) && errors.Is(currentErr, os.ErrNotExist):
		default:
			return placementError("preserved an unexpected resource beside an unfinished purge capture")
		}
		control.Pending = nil
		return writePlacementControl(guard, *control)
	})
}

func purgeFileDigestV1(ctx context.Context, file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	buffer := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := file.Read(buffer)
		if n != 0 {
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(err, io.EOF) {
			return hex.EncodeToString(hash.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
	}
}

func checkPurgeResourceFileV1(ctx context.Context, resource *purgeResource, file *os.File) error {
	if resource == nil || !supply.IsHex64(resource.SHA256) {
		return placementError("the pending resource has no original content witness")
	}
	info, err := file.Stat()
	if err != nil || !resource.Ref.matches(info) {
		return placementError("the pending resource changed its native identity")
	}
	digest, err := purgeFileDigestV1(ctx, file)
	if err != nil {
		return err
	}
	if digest != resource.SHA256 {
		return placementError("the pending resource changed its original contents")
	}
	if resource.Item != nil {
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := supply.CheckOpenContext(ctx, *resource.Item, file); err != nil {
			return err
		}
	}
	after, err := file.Stat()
	if err != nil || !resource.Ref.matches(after) {
		return placementError("the pending resource changed during its content check")
	}
	return ctx.Err()
}

func purgeSafeParentRefV1(ref purgeRef) bool {
	return len(ref.extra) == 0 && ref.Inode != 0 && ref.UID == uint64(os.Getuid()) && os.FileMode(ref.Mode).IsDir() && os.FileMode(ref.Mode).Perm()&0o022 == 0
}

func removeCapturedOwnedFileV1(root *os.Root, leaf string, expected purgeRef, capture string, record func(string) error, check func(*os.File) error) error {
	mode := os.FileMode(expected.Mode).Perm()
	file, err := cachefs.OpenOwnedFileExisting(root, leaf, mode)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := cachefs.ValidateOwnedFile(root, leaf, file, mode)
	if err != nil || !expected.matches(info) {
		return placementError("the owned file changed before its native capture")
	}
	if check != nil {
		if err := check(file); err != nil {
			return err
		}
	}
	if err := record(capture); err != nil {
		return err
	}
	if err := cachefs.RenameOwnedFileNoReplace(root, file, leaf, capture, mode); err != nil {
		return err
	}
	info, err = cachefs.ValidateOwnedFile(root, capture, file, mode)
	if err == nil && expected.matches(info) && check != nil {
		err = check(file)
	}
	if err != nil || !expected.matches(info) {
		return errors.Join(placementError("preserved a changed owned-file capture"), err, cachefs.RenameOwnedFileNoReplace(root, file, capture, leaf, mode))
	}
	if err := record(capture); err != nil {
		return err
	}
	info, err = cachefs.ValidateOwnedFile(root, capture, file, mode)
	if err != nil || !expected.matches(info) {
		return placementError("the captured owned file changed before unlink")
	}
	if err := root.Remove(capture); err != nil {
		return err
	}
	return syncCapturedDirectory(root)
}
