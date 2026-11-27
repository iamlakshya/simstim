//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/slotlaunch"
	"github.com/iamlakshya/simstim/internal/vmneed"
	"golang.org/x/sys/unix"
)

type purgeStoppedCleanupKeyV1 struct{}

type stoppedCleanupOwnerV1 struct {
	purge      *purgeOwnerV1
	operation  *SessionOperation
	stopped    *StoppedSessionV1
	ordinary   *StoppedOrdinarySessionV1
	root       *os.Root
	id         string
	member     *stoppedMemberRootV1
	mount      uint64
	files      int
	leases     []*os.File
	leaseNames map[string]bool
}

// DestroyStoppedSessionResourcesV1 is a storage consumer of an actual opaque
// physical stop. It creates no boot context, driver, source or native lifetime.
// The protected primary root and fixed resource layout narrow every removal.
func DestroyStoppedSessionResourcesV1(ctx context.Context, operation *SessionOperation, stopped *StoppedSessionV1) error {
	purge, _ := ctx.Value(purgeStoppedCleanupKeyV1{}).(*purgeOwnerV1)
	if purge == nil || operation == nil || stopped == nil || stopped.operation != operation || stopped.origin != purge.placement {
		return sessionLeaseError("stopped cleanup has no same original purge, operation and native owner")
	}
	if err := stopped.Check(ctx, operation); err != nil {
		return err
	}
	owner := &stoppedCleanupOwnerV1{purge: purge, operation: operation, stopped: stopped, root: operation.native.root, id: operation.Primary()}
	defer func() {
		for _, lease := range owner.leases {
			_ = lease.Close()
		}
	}()
	root := operation.native.root
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	owner.mount, err = stoppedCleanupMountV1(directory)
	_ = directory.Close()
	if err != nil {
		return err
	}
	source := CloneManifest(stopped.source)
	if source.Wall == nil {
		return sessionLeaseError("the actual stopped source has no supported wall")
	}
	if len(source.extra) != 0 {
		return sessionLeaseError("future protected session fields grant no cleanup authority")
	}
	if err := owner.removeMemberRoots(ctx); err != nil {
		return err
	}
	owner.leaseNames = map[string]bool{}
	backendLease := ""
	switch source.Wall.Level {
	case "gvisor":
		backendLease = "gvisor.lock"
	case "microvm":
		backendLease = source.Wall.Driver + ".lock"
	}
	if backendLease != "" {
		lease, err := cachefs.OpenFile(root, backendLease, os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		held, err := cachefs.TryLock(lease, cachefs.Exclusive)
		if err != nil || !held {
			_ = lease.Close()
			return sessionLeaseError("the original stopped backend still has another native storage owner")
		}
		owner.leases = append(owner.leases, lease)
		owner.leaseNames[backendLease] = true
	}
	if source.Wall.Level == "microvm" {
		if err := owner.destroyVMStorage(ctx, source); err != nil {
			return err
		}
	}
	entries, err := stoppedCleanupEntriesV1(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "manifest.json" || name == lockFile || name == firstLockFile || name == holderLockFile {
			continue
		}
		if !knownStoppedRootResourceV1(name, source.Wall.Level) {
			return sessionLeaseError("the stopped root contains an unknown resource: " + name)
		}
		if err := owner.removeResource(ctx, name, 0); err != nil {
			return err
		}
	}
	// Socket names are fixed by the actual same protected primary. No path from
	// a guest claim, VM record or host report is used as cleanup authority.
	for _, path := range []string{paths.Socket(source.ID), paths.HookSocket(source.ID)} {
		if err := owner.removeSocket(ctx, path); err != nil {
			return err
		}
	}
	return stopped.Check(ctx, operation)
}

func knownStoppedRootResourceV1(name, level string) bool {
	switch name {
	case "holder.log", "holder.log.1", "debug.jsonl", "debug.jsonl.1", "audit.jsonl", "audit.jsonl.1", "events.jsonl", "events.jsonl.1", "namespace.log", "rename.unsaved", "seed.bundle", "seed-includes", "work", "state", "home", "members", "paste", "pull", "debug", "inputs":
		return true
	case "process.json", "process-root":
		return level == "process"
	case "gvisor", "gvisor.lock":
		return level == "gvisor"
	case "vm", "chv.json", "chv.lock", "firecracker.json", "firecracker.lock", "nitro.json", "nitro.lock", "fc.json":
		return level == "microvm"
	}
	return false
}

func (owner *stoppedCleanupOwnerV1) check(ctx context.Context) error {
	if owner == nil || owner.operation == nil || owner.purge == nil {
		return sessionLeaseError("the original stopped storage owner ended")
	}
	if owner.ordinary != nil {
		if owner.stopped != nil || owner.member != nil || owner.ordinary.origin != owner.purge.placement {
			return sessionLeaseError("the ordinary storage owner has another native source or resource role")
		}
		return owner.ordinary.Check(ctx, owner.operation)
	}
	if owner.stopped == nil {
		return sessionLeaseError("the original stopped storage owner ended")
	}
	if err := owner.stopped.Check(ctx, owner.operation); err != nil {
		return err
	}
	if owner.member != nil {
		return owner.member.check(ctx)
	}
	return nil
}

func (owner *stoppedCleanupOwnerV1) destroyVMStorage(ctx context.Context, source Manifest) error {
	if owner == nil || owner.stopped == nil || owner.ordinary != nil || owner.member != nil {
		return sessionLeaseError("only the original walled whole-stop source can settle VM storage")
	}
	need, err := vmneed.DecodeV1(source.VMNeedV1, runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := need.ValidateWall(runtime.GOARCH, source.Wall.Level, source.Wall.Driver, source.Limits.MemoryMax); err != nil {
		return err
	}
	leaf := need.Driver + ".json"
	file, err := cachefs.OpenFile(owner.operation.native.root, leaf, os.O_RDONLY, 0o600)
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 1025))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(raw) > 1024 {
		return errors.Join(sessionLeaseError("the stopped VM layout is incomplete or oversized"), readErr, closeErr)
	}
	var layout struct {
		Version   int    `json:"version"`
		Mode      string `json:"uid_mode"`
		ID        string `json:"id"`
		Directory string `json:"session_dir"`
		Disk      int64  `json:"disk_max"`
	}
	if !api.ExactJSONFields(raw, layout) || json.Unmarshal(raw, &layout) != nil || layout.Version != 1 || layout.ID != source.ID || layout.Directory != owner.operation.native.path || layout.Disk != need.DiskMax || layout.Mode != "login" && layout.Mode != "subuid" && layout.Mode != "own-account" {
		return sessionLeaseError("the stopped VM layout conflicts with its original protected Need")
	}
	if err := owner.check(ctx); err != nil {
		return err
	}
	if layout.Mode == "own-account" {
		// This fixed optional launcher authenticates its caller and original root
		// record, locks the actual slot and proves all native lifetime absence.
		if err := slotlaunch.DestroyNativeV1(ctx, source.ID); err != nil {
			return err
		}
		return owner.check(ctx)
	}
	// The ordinary/subuid lane uses only the SAME protected root. A foreign UID,
	// mount or inaccessible tree is preserved; no boot-capable Driver.Open is used.
	return nil
}

func stoppedCleanupEntriesV1(root *os.Root) ([]os.DirEntry, error) {
	file, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := file.ReadDir(-1)
	return entries, errors.Join(err, file.Close())
}

func stoppedCleanupMountV1(file *os.File) (uint64, error) {
	var stat unix.Statx_t
	if err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &stat); err != nil {
		return 0, err
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 {
		return 0, sessionLeaseError("the stopped resource has no native mount identity")
	}
	return stat.Mnt_id, nil
}

func (owner *stoppedCleanupOwnerV1) removeResource(ctx context.Context, relative string, depth int) error {
	if depth > 128 || owner.files >= 100000 {
		return sessionLeaseError("the stopped resource tree exceeds its inspection bound")
	}
	owner.files++
	if err := owner.check(ctx); err != nil {
		return err
	}
	root := owner.root
	info, err := root.Lstat(relative)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	ref := refOf(info)
	if ref.UID != uint64(os.Getuid()) || info.Mode().Perm()&0o022 != 0 || info.Mode()&os.ModeSymlink != 0 {
		return sessionLeaseError("preserved a linked, foreign or writable stopped resource: " + relative)
	}
	if info.IsDir() {
		child, err := root.OpenRoot(relative)
		if err != nil {
			return err
		}
		directory, err := child.Open(".")
		if err != nil {
			_ = child.Close()
			return err
		}
		current, statErr := directory.Stat()
		mount, mountErr := stoppedCleanupMountV1(directory)
		entries, readErr := directory.ReadDir(-1)
		_ = directory.Close()
		_ = child.Close()
		if statErr != nil || mountErr != nil || readErr != nil || !ref.matches(current) || mount != owner.mount {
			return sessionLeaseError("preserved a changed or mounted stopped resource tree: " + relative)
		}
		for _, entry := range entries {
			if err := owner.removeResource(ctx, filepath.Join(relative, entry.Name()), depth+1); err != nil {
				return err
			}
		}
		key := "stopped-directory:" + owner.id + "/" + relative
		if err := owner.purge.gate(func(control *placementControl, _ *os.Root) error {
			if control.Refs == nil {
				control.Refs = map[string]purgeRef{}
			}
			if prior, ok := control.Refs[key]; ok && !samePurgeDirectoryRef(prior, ref) {
				return sessionLeaseError("the stopped directory changed its original cleanup generation")
			}
			control.Refs[key] = ref
			return nil
		}); err != nil {
			return err
		}
		if err := owner.check(ctx); err != nil {
			return err
		}
		return owner.purge.removeEmptyDirectory(filepath.Join(root.Name(), relative), ref)
	}
	if !info.Mode().IsRegular() {
		return sessionLeaseError("preserved an unknown stopped resource type: " + relative)
	}
	if stoppedResourceIsLeaseV1(filepath.Base(relative)) && !owner.leaseNames[relative] {
		lease, err := root.OpenFile(relative, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		if _, err := cachefs.ValidateDescriptor(lease, info.Mode().Perm()); err != nil {
			_ = lease.Close()
			return err
		}
		held, err := cachefs.TryLock(lease, cachefs.Exclusive)
		if err != nil || !held {
			_ = lease.Close()
			return sessionLeaseError("the stopped backend retains another original storage lease")
		}
		owner.leases = append(owner.leases, lease)
	}
	parent, err := cachefs.OpenSafeAncestor(filepath.Join(root.Name(), filepath.Dir(relative)))
	if err != nil {
		return err
	}
	file, err := cachefs.OpenOwnedFileExisting(parent, filepath.Base(relative), info.Mode().Perm())
	if err != nil {
		_ = parent.Close()
		return err
	}
	native, err := file.Stat()
	mount, mountErr := stoppedCleanupMountV1(file)
	_ = file.Close()
	_ = parent.Close()
	if err != nil || mountErr != nil || !ref.matches(native) || mount != owner.mount {
		return sessionLeaseError("preserved a changed or mounted stopped file: " + relative)
	}
	return owner.purge.removeKnownFile("state", filepath.Join("sessions", owner.id, relative), ref, func(*os.File) error { return owner.check(ctx) })
}

func (owner *stoppedCleanupOwnerV1) removeSocket(ctx context.Context, path string) error {
	if err := owner.check(ctx); err != nil {
		return err
	}
	parent, err := cachefs.OpenPrivateRoot(filepath.Dir(path), false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer parent.Close()
	leaf := filepath.Base(path)
	capture := ".purge-socket-" + owner.purge.args.Nonce + "-" + leaf
	info, err := parent.Lstat(leaf)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	ref := refOf(info)
	if info.Mode()&os.ModeSocket == 0 || ref.UID != uint64(os.Getuid()) {
		return sessionLeaseError("preserved an unknown fixed session socket")
	}
	if err := cachefs.RenameNoReplace(parent, leaf, capture); err != nil {
		return err
	}
	current, err := parent.Lstat(capture)
	if err != nil || !ref.matches(current) {
		return errors.Join(sessionLeaseError("preserved a replaced captured session socket"), cachefs.RenameNoReplace(parent, capture, leaf))
	}
	if err := owner.check(ctx); err != nil {
		return err
	}
	if err := parent.Remove(capture); err != nil {
		return err
	}
	return syncCapturedDirectory(parent)
}

func stoppedResourceIsLeaseV1(name string) bool { return strings.HasSuffix(name, ".lock") }

// The terminal metadata consumer keeps the real holder vacancy and original
// OP descriptors until every runtime resource has gone. It never reacquires a
// replacement lease after unlinking the original canonical OP name.
func finishStoppedSessionMetadataV1(ctx context.Context, purge *purgeOwnerV1, operation *SessionOperation, stopped *StoppedSessionV1) error {
	if err := stopped.Check(ctx, operation); err != nil {
		return err
	}
	root := operation.native.root
	entries, err := stoppedCleanupEntriesV1(root)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"manifest.json": true, lockFile: true, firstLockFile: true, holderLockFile: true}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return sessionLeaseError("the stopped root retains an unsettled resource: " + entry.Name())
		}
	}
	first, err := cachefs.OpenFile(root, firstLockFile, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer first.Close()
	held, err := cachefs.TryLock(first, cachefs.Exclusive)
	if err != nil || !held {
		return sessionLeaseError("the original first-save lifetime is still owned or uncertain")
	}
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	dirInfo, err := directory.Stat()
	_ = directory.Close()
	if err != nil {
		return err
	}
	manifest, err := cachefs.OpenFile(root, "manifest.json", os.O_RDONLY, 0o600)
	if err != nil {
		return err
	}
	manifestInfo, err := cachefs.ValidateFile(root, "manifest.json", manifest, 0o600)
	_ = manifest.Close()
	if err != nil {
		return err
	}
	// This is the last complete protected-source check. The following private
	// phase retains those SAME kernel objects and checks physical facts afresh;
	// removing the manifest is never interpreted as evidence that a process died.
	if err := stopped.Check(ctx, operation); err != nil {
		return err
	}
	if err := purge.removeFile("state", filepath.Join("sessions", operation.Primary(), "manifest.json"), refOf(manifestInfo), func(*os.File) error { return stopped.Check(ctx, operation) }); err != nil {
		return err
	}
	terminal := []struct {
		name string
		file *os.File
	}{{firstLockFile, first}, {holderLockFile, stopped.holder.file}, {lockFile, operation.native.file}}
	for _, entry := range terminal {
		if err := errors.Join(ctx.Err(), stopped.checkStoppedNativeFactsV1(ctx), operation.native.rootRef.CheckRoot(root)); err != nil {
			return err
		}
		info, err := cachefs.ValidateFile(root, entry.name, entry.file, 0o600)
		if err != nil || info.Size() != 0 {
			return sessionLeaseError("the original terminal session lease changed its native role")
		}
		ref := refOf(info)
		capture := ".session-final-" + purge.args.Nonce + "-" + entry.name
		if err := removeCapturedFile(root, entry.name, ref, capture, func(string) error {
			return errors.Join(ctx.Err(), stopped.checkStoppedNativeFactsV1(ctx), operation.native.rootRef.CheckRoot(root))
		}, nil); err != nil {
			return err
		}
	}
	if err := errors.Join(ctx.Err(), stopped.checkStoppedNativeFactsV1(ctx), operation.native.rootRef.CheckRoot(root)); err != nil {
		return err
	}
	return purge.removeEmptyDirectory(operation.native.path, refOf(dirInfo))
}
