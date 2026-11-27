//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/paths"
)

// Member storage has no independent stop constructor. Its live owner retains
// the original whole-primary stopped source, original creation/group receipt
// and the exact child locks until that child's final directory removal.
type stoppedMemberRootV1 struct {
	parent  *stoppedCleanupOwnerV1
	id      string
	receipt memberRootReceiptV1
	locks   []*sessionFileLease
}

func (owner *stoppedCleanupOwnerV1) removeMemberRoots(ctx context.Context) (returned error) {
	if owner == nil || owner.purge == nil || owner.operation == nil || owner.stopped == nil || owner.member != nil || owner.id != owner.operation.Primary() {
		return sessionLeaseError("only the original stopped primary can settle member roots")
	}
	if err := owner.check(ctx); err != nil {
		return err
	}
	var receipts map[string]memberRootReceiptV1
	if err := owner.purge.gate(func(control *placementControl, _ *os.Root) error {
		receipts = make(map[string]memberRootReceiptV1, len(control.MemberRoots))
		for id, receipt := range control.MemberRoots {
			if receipt.Primary == owner.id {
				receipts[id] = receipt
			}
		}
		return nil
	}); err != nil {
		return err
	}
	members, err := SandboxMembers(owner.id)
	if err != nil {
		return err
	}
	for _, member := range members {
		receipt, exists := receipts[member.ID]
		if !exists || !member.CreatedAt.Equal(receipt.CreatedAt) {
			owner.purge.reportItem(false, "session", member.ID, "the member has no original native group and directory creation receipt")
			return sessionLeaseError("a stopped member has no original native group and directory creation receipt")
		}
	}
	ids := make([]string, 0, len(receipts))
	for id := range receipts {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	// Pin every recorded root before deleting any member. A changed or unknown
	// member cannot cause partial acceptance of its siblings' creation claims.
	pinned := make([]*stoppedMemberRootV1, 0, len(ids))
	defer func() {
		for _, member := range pinned {
			returned = errors.Join(returned, member.close())
		}
	}()
	for _, id := range ids {
		member, err := owner.openMemberRoot(ctx, id, receipts[id])
		if err != nil {
			owner.purge.reportItem(false, "session", id, err.Error())
			return err
		}
		if member != nil {
			pinned = append(pinned, member)
		}
	}
	for _, member := range pinned {
		if err := member.remove(ctx); err != nil {
			return err
		}
	}
	return owner.check(ctx)
}

func (owner *stoppedCleanupOwnerV1) openMemberRoot(ctx context.Context, id string, receipt memberRootReceiptV1) (result *stoppedMemberRootV1, returned error) {
	if err := owner.check(ctx); err != nil {
		return nil, err
	}
	if err := validateMemberRootReceiptsV1(map[string]memberRootReceiptV1{id: receipt}); err != nil {
		return nil, err
	}
	if receipt.Primary != owner.id {
		return nil, sessionLeaseError("the member receipt belongs to another original primary")
	}
	if err := receipt.PrimaryRef.matchesRoot(owner.root); err != nil {
		return nil, err
	}
	root, err := cachefs.OpenPrivateRoot(paths.SessionDir(id), false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := receipt.Directory.matchesRoot(root); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	if _, err := readMemberRootManifestV1(root, id, receipt); err != nil {
		return nil, errors.Join(err, root.Close())
	}
	if err := root.Close(); err != nil {
		return nil, err
	}
	member := &stoppedMemberRootV1{parent: owner, id: id, receipt: receipt}
	defer func() {
		if result == nil {
			returned = errors.Join(returned, member.close())
		}
	}()
	for _, role := range []string{lockFile, firstLockFile, holderLockFile} {
		lease, err := openOwnedSessionFileLease(ctx, id, role, time.Second)
		if err != nil {
			return nil, err
		}
		member.locks = append(member.locks, lease)
		if err := receipt.Directory.matchesRoot(lease.root); err != nil {
			return nil, err
		}
	}
	if err := member.check(ctx); err != nil {
		return nil, err
	}
	return member, nil
}

func (member *stoppedMemberRootV1) check(ctx context.Context) error {
	if member == nil || member.parent == nil || len(member.locks) != 3 {
		return sessionLeaseError("the original stopped member storage owner ended")
	}
	if err := member.parent.check(ctx); err != nil {
		return err
	}
	for _, lease := range member.locks {
		if err := lease.check(ctx); err != nil {
			return err
		}
		if err := member.receipt.Directory.matchesRoot(lease.root); err != nil {
			return err
		}
	}
	return nil
}

func (member *stoppedMemberRootV1) close() error {
	var err error
	for _, lease := range member.locks {
		err = errors.Join(err, lease.Close())
	}
	member.locks = nil
	return err
}

func (member *stoppedMemberRootV1) remove(ctx context.Context) error {
	if err := member.check(ctx); err != nil {
		return err
	}
	root := member.locks[0].root
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	mount, err := stoppedCleanupMountV1(directory)
	closeErr := directory.Close()
	if err != nil || closeErr != nil || mount != member.parent.mount {
		return errors.Join(sessionLeaseError("the original member root changed its native mount"), err, closeErr)
	}
	storage := &stoppedCleanupOwnerV1{purge: member.parent.purge, operation: member.parent.operation, stopped: member.parent.stopped, root: root, id: member.id, member: member, mount: mount, leaseNames: map[string]bool{}}
	defer func() {
		for _, lease := range storage.leases {
			_ = lease.Close()
		}
	}()
	entries, err := stoppedCleanupEntriesV1(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "manifest.json" || name == lockFile || name == firstLockFile || name == holderLockFile {
			continue
		}
		if !knownStoppedMemberResourceV1(name) {
			return sessionLeaseError("the member root contains an unknown resource: " + name)
		}
		if err := storage.removeResource(ctx, name, 0); err != nil {
			return err
		}
	}
	for _, path := range []string{paths.Socket(member.id), paths.HookSocket(member.id)} {
		if err := storage.removeSocket(ctx, path); err != nil {
			return err
		}
	}
	manifest, err := cachefs.OpenFile(root, "manifest.json", os.O_RDONLY, 0o600)
	if err == nil {
		info, checked := cachefs.ValidateFile(root, "manifest.json", manifest, 0o600)
		closeErr := manifest.Close()
		if checked != nil || closeErr != nil {
			return errors.Join(checked, closeErr)
		}
		if _, err := readMemberRootManifestV1(root, member.id, member.receipt); err != nil {
			return err
		}
		if err := storage.purge.removeFile("state", filepath.Join("sessions", member.id, "manifest.json"), refOf(info), func(*os.File) error { return member.check(ctx) }); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// All three SAME child leases stay held after their names are removed.
	// Physical whole-primary closure is checked afresh; absent metadata is never
	// interpreted as proof that a member process ended.
	for _, lease := range member.locks {
		if err := member.checkTerminal(ctx); err != nil {
			return err
		}
		info, err := cachefs.ValidateFile(root, lease.name, lease.file, 0o600)
		if err != nil || info.Size() != 0 {
			return sessionLeaseError("the original member terminal lease changed its role")
		}
		capture := ".member-final-" + storage.purge.args.Nonce + "-" + lease.name
		if err := removeCapturedFile(root, lease.name, refOf(info), capture, func(string) error { return member.checkTerminal(ctx) }, nil); err != nil {
			return err
		}
	}
	if err := member.checkTerminal(ctx); err != nil {
		return err
	}
	if err := storage.purge.removeEmptyDirectory(paths.SessionDir(member.id), member.receipt.Directory); err != nil {
		return err
	}
	storage.purge.reportItem(true, "session", member.id, "")
	return nil
}

func (member *stoppedMemberRootV1) checkTerminal(ctx context.Context) error {
	if member == nil || member.parent == nil || len(member.locks) != 3 {
		return sessionLeaseError("the original member terminal owner ended")
	}
	if err := member.parent.check(ctx); err != nil {
		return err
	}
	for _, lease := range member.locks {
		if err := member.receipt.Directory.matchesRoot(lease.root); err != nil {
			return err
		}
		if _, err := lease.file.Stat(); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func knownStoppedMemberResourceV1(name string) bool {
	switch name {
	case "holder.log", "holder.log.1", "debug.jsonl", "debug.jsonl.1", "audit.jsonl", "audit.jsonl.1", "events.jsonl", "events.jsonl.1", "rename.unsaved", "paste", "pull", "debug", "inputs":
		return true
	}
	return false
}
