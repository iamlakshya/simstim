package host

import (
	"errors"
	"os"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/proto"
)

// writeOwnedMetadata is used only under the caller's canonical ownership lock.
// The caller validates the existing schema before supplying its exact info.
func writeOwnedMetadata(root *os.Root, leaf string, data []byte, before os.FileInfo) (os.FileInfo, error) {
	return writeOwnedMetadataIntent(root, leaf, data, before, nil)
}

// writeOwnedMetadataIntent exposes the prepared inode before publication so
// a caller can fsync its ahead journal under the already-held ownership lock.
func writeOwnedMetadataIntent(root *os.Root, leaf string, data []byte, before os.FileInfo, prepared func(os.FileInfo, string) error) (os.FileInfo, error) {
	if len(data) > proto.MaxFrame {
		return nil, placementError("the metadata exceeds the protocol bound")
	}
	return writeOwnedMetadataComputed(root, leaf, before, func(os.FileInfo) ([]byte, error) { return data, nil }, prepared)
}

// writeOwnedMetadataComputed binds a prepared native identity before encoding
// final control data. It shares every creation, CAS, sync and cleanup check.
func writeOwnedMetadataComputed(root *os.Root, leaf string, before os.FileInfo, encode func(os.FileInfo) ([]byte, error), prepared func(os.FileInfo, string) error) (os.FileInfo, error) {
	nonce, err := footprintNonce()
	if err != nil {
		return nil, err
	}
	name := ".metadata-" + nonce
	file, err := cachefs.OpenFile(root, name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() {
		if _, err := cachefs.ValidateFile(root, name, file, 0o600); err == nil {
			_ = root.Remove(name)
		}
		file.Close()
	}()
	info, err := cachefs.ValidateFile(root, name, file, 0o600)
	if err != nil {
		return nil, err
	}
	data, err := encode(info)
	if err != nil {
		return nil, err
	}
	if len(data) > proto.MaxFrame {
		return nil, placementError("the metadata exceeds the protocol bound")
	}
	if _, err := file.Write(data); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	if prepared != nil {
		info, err := cachefs.ValidateFile(root, name, file, 0o600)
		if err != nil {
			return nil, err
		}
		if err := prepared(info, name); err != nil {
			return nil, err
		}
	}
	if before == nil {
		if err := cachefs.RenameNoReplace(root, name, leaf); err != nil {
			return nil, err
		}
	} else {
		current, err := cachefs.OpenFile(root, leaf, os.O_RDONLY, 0o600)
		if err != nil {
			return nil, err
		}
		info, err := current.Stat()
		current.Close()
		if err != nil || !os.SameFile(before, info) || !refOf(before).matches(info) {
			return nil, placementError("the metadata destination changed before publication")
		}
		if err := root.Rename(name, leaf); err != nil {
			return nil, err
		}
	}
	info, err = cachefs.ValidateFile(root, leaf, file, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syncCapturedDirectory(root); err != nil {
		return nil, err
	}
	return info, nil
}

func metadataBefore(root *os.Root, leaf string) (os.FileInfo, error) {
	file, err := cachefs.OpenFile(root, leaf, os.O_RDONLY, 0o600)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}
