//go:build linux

package guest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/filecopy"
	"github.com/iamlakshya/simstim/internal/proto"
)

// vmCopyMountV1 is constructed ONLY beside the actual vmFiles mount/bind
// producer before pivot or user commands. These are the ORIGINAL mount's
// handles; neither a guest pathname nor an inode/device tuple constructs one.
type vmCopyMountV1 struct {
	prefix       string
	root, stages *os.Root
}

type vmCopyStageRootV1 struct {
	parent   *os.Root
	name     string
	identity os.FileInfo
}

type vmFileCommitV1 struct {
	owner               *vmRuntime
	mount               *vmCopyMountV1
	stage               *os.Root
	stageName           string
	identity            os.FileInfo
	file                *os.File
	fileIdentity        os.FileInfo
	header              proto.FilePutHeaderV1
	guestPath, relative string
	mu                  sync.Mutex
	closed              bool
}

func privateCopyDirectory(root *os.Root, name string, uid, gid int) (*os.Root, error) {
	if err := filecopy.Name(name); err != nil {
		return nil, err
	}
	if err := root.Mkdir(name, 0700); err != nil {
		return nil, err
	}
	opened, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	file, err := opened.Open(".")
	if err != nil {
		return nil, errors.Join(err, opened.Close())
	}
	if err = file.Chown(uid, gid); err != nil {
		return nil, errors.Join(err, file.Close(), opened.Close())
	}
	if err = file.Chmod(0700); err != nil {
		return nil, errors.Join(err, file.Close(), opened.Close())
	}
	if err = file.Close(); err != nil {
		return nil, errors.Join(err, opened.Close())
	}
	return opened, nil
}

func copyNonce() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

// captureCopyMountV1 receives the producer's actual original backing root,
// its visible subdirectory and root-owned private stage parent. No wire DTO
// reaches this function. Each retained handle owns its independent close.
func (v *vmFiles) captureCopyMountV1(prefix string, backing *os.Root, visible, stageParent string) error {
	if v.copyNativeOwner == nil {
		return nil
	} // Metadata-only layout has no original commit capability.
	if !path.IsAbs(prefix) || path.Clean(prefix) != prefix || backing == nil {
		return vmError("the actual copy mount producer is incomplete")
	}
	root, err := backing.OpenRoot(visible)
	if err != nil {
		return err
	}
	stages, err := backing.OpenRoot(stageParent)
	if err != nil {
		return errors.Join(err, root.Close())
	}
	if v.copyMounts == nil {
		v.copyMounts = make(map[string]*vmCopyMountV1)
	}
	if v.copyMounts[prefix] != nil {
		return errors.Join(vmError("an original writable copy mount was already retained"), root.Close(), stages.Close())
	}
	v.copyMounts[prefix] = &vmCopyMountV1{prefix: prefix, root: root, stages: stages}
	return nil
}

// prepareCopyStagesV1 is called while only the real native VM initializer
// exists. A unique root-owned parent stays outside every visible writable
// subtree. It is not a same-UID private-directory claim.
func (v *vmFiles) prepareCopyStagesV1(backing *os.Root) (string, error) {
	if v.copyNativeOwner == nil {
		return "", nil
	}
	name, err := copyNonce()
	if err != nil {
		return "", err
	}
	name = ".copy-" + name
	root, err := privateCopyDirectory(backing, name, 0, 0)
	if err != nil {
		return "", err
	}
	identity, err := root.Stat(".")
	if err != nil {
		return "", errors.Join(err, root.Close())
	}
	parent, err := backing.OpenRoot(".")
	if err != nil {
		return "", errors.Join(err, root.Close())
	}
	v.copyStageRoots = append(v.copyStageRoots, vmCopyStageRootV1{parent: parent, name: name, identity: identity})
	return name, root.Close()
}

func (v *vmRuntime) beginFileCommitV1(ctx context.Context, header proto.FilePutHeaderV1) (_ *vmFileCommitV1, failure error) {
	if err := v.connection.check(v.init); err != nil {
		return nil, err
	}
	if !v.prepared() || v.files == nil {
		return nil, vmError("the original VM copy resources are not prepared")
	}
	if v.files.copyNativeOwner != v.connection {
		return nil, vmError("the copy mount resources have no same original native initializer")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	top, relative, err := putTarget(header)
	if err != nil || header.Directory || header.Path == "" {
		return nil, putError("the owned VM file commit needs one explicit file target")
	}
	guestPath := path.Join(top, relative)
	var selected *vmCopyMountV1
	for _, mount := range v.files.copyMounts {
		if guestPath == mount.prefix || strings.HasPrefix(guestPath, mount.prefix+"/") {
			if selected == nil || len(mount.prefix) > len(selected.prefix) {
				selected = mount
			}
		}
	}
	if selected == nil {
		return nil, putError("the target has no original writable VM mount resource")
	}
	relative = strings.TrimPrefix(guestPath, selected.prefix+"/")
	if relative == guestPath || filecopy.Relative(relative) != nil {
		return nil, putError("the owned copy target is not a relative file")
	}
	name, err := copyNonce()
	if err != nil {
		return nil, err
	}
	stage, err := privateCopyDirectory(selected.stages, name, AgentUID, AgentGID)
	if err != nil {
		return nil, err
	}
	identity, err := stage.Stat(".")
	if err != nil {
		return nil, errors.Join(err, stage.Close())
	}
	transaction := &vmFileCommitV1{owner: v, mount: selected, stage: stage, stageName: name, identity: identity, header: header, guestPath: guestPath, relative: relative}
	defer func() {
		if failure != nil {
			failure = errors.Join(failure, transaction.close())
		}
	}()
	file, err := stage.OpenFile("data", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	transaction.file = file
	fileIdentity, err := file.Stat()
	if err != nil {
		return nil, err
	}
	transaction.fileIdentity = fileIdentity
	if err = file.Chown(AgentUID, AgentGID); err != nil {
		return nil, err
	}
	return transaction, nil
}

func (t *vmFileCommitV1) close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	// Only this root-owned parent's unreachable per-operation directory is
	// touched, after the actual commit helper joined. No caller path, prefix
	// sweep or foreign visible destination is removed.
	var result error
	if t.file != nil {
		result = errors.Join(result, t.file.Close())
	}
	if info, err := t.stage.Lstat("data"); err == nil {
		if t.fileIdentity != nil && os.SameFile(info, t.fileIdentity) {
			result = errors.Join(result, t.stage.Remove("data"))
		} else {
			result = errors.Join(result, putError("the private copy stage changed before cleanup"))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(result, err)
	}
	current, err := t.mount.stages.Lstat(t.stageName)
	if err == nil && os.SameFile(current, t.identity) {
		result = errors.Join(result, t.mount.stages.Remove(t.stageName))
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(result, err)
	}
	return errors.Join(result, t.stage.Close())
}

func (v *vmFiles) closeCopyMountsV1() error {
	var result error
	for key, mount := range v.copyMounts {
		result = errors.Join(result, mount.root.Close(), mount.stages.Close())
		delete(v.copyMounts, key)
	}
	for _, stage := range v.copyStageRoots {
		info, err := stage.parent.Lstat(stage.name)
		if err == nil && os.SameFile(info, stage.identity) {
			result = errors.Join(result, stage.parent.Remove(stage.name))
		} else if !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err, putError("the original private copy directory changed before release"))
		}
		result = errors.Join(result, stage.parent.Close())
	}
	v.copyStageRoots = nil
	return result
}
