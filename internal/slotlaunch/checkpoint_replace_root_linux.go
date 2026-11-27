//go:build linux

package slotlaunch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/checkpoint"
	"golang.org/x/sys/unix"
)

func (b *backend) replaceCheckpointReservedV2(ctx context.Context, r Request) (Response, error) {
	if !nativeSocket(os.Stdout) {
		return Response{}, refused("reserved replacement needs its actual native channel")
	}
	record, lock, err := b.session(r.Session, false)
	if err != nil {
		return Response{}, err
	}
	defer lock.Close()
	store, err := b.lock("store/.lock", false)
	if err != nil {
		return Response{}, err
	}
	defer store.Close()
	initial, err := checkpointNativeReplyV1(Response{OK: true})
	if err != nil {
		return Response{}, err
	}
	err = sendNativeResponse(initial)
	initial.native.close()
	if err != nil {
		return Response{}, err
	}
	count := 21
	if r.Op == "checkpoint.prepare-v2" {
		count = 20
	}
	roles, err := receiveCheckpointReadFilesV1(count)
	if err != nil {
		return Response{}, err
	}
	defer func() {
		for _, file := range roles {
			file.Close()
		}
	}()
	if err := b.checkReservedSlotCoreV2(record, roles); err != nil {
		return Response{}, err
	}
	if err := requireOriginalSlotActorsExitedV2(roles); err != nil {
		return Response{}, err
	}
	if err := b.checkSelectedReservedV2(r, roles); err != nil {
		return Response{}, err
	}
	if r.Op == "checkpoint.prepare-v2" {
		return b.prepareReservedReplacementV2(ctx, r, record, roles)
	}
	if r.MetaSize > 0 {
		metadata := make([]byte, r.MetaSize)
		if _, err := io.ReadFull(os.Stdin, metadata); err != nil {
			return Response{}, err
		}
		wanted, err := checkpoint.DecodeMetadataV1(metadata)
		if err != nil {
			return Response{}, err
		}
		files, err := b.selectCheckpointV1(r.Session, r.Safety, roles[16])
		if err != nil {
			return Response{}, err
		}
		actual, err := readCheckpointMetadataFDV2(files[1])
		for _, file := range files {
			file.Close()
		}
		if err != nil {
			return Response{}, err
		}
		left, _ := checkpoint.EncodeMetadataV1(wanted)
		right, _ := checkpoint.EncodeMetadataV1(actual)
		if !bytes.Equal(left, right) {
			return Response{}, refused("the original safety metadata changed during its actual reserved transaction")
		}
	}
	return b.changeReservedReplacementV2(ctx, r, record, roles)
}

func (b *backend) checkSelectedReservedV2(r Request, roles []*os.File) error {
	files, err := b.selectCheckpointV1(r.Session, r.Name, roles[16])
	if err != nil {
		return err
	}
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	for index, current := range files {
		want, err := current.Stat()
		if err != nil {
			return err
		}
		got, err := roles[18+index].Stat()
		flags, flagErr := unix.FcntlInt(roles[18+index].Fd(), unix.F_GETFL, 0)
		if err != nil || flagErr != nil || flags&unix.O_ACCMODE != unix.O_RDONLY || !singleRegular(got, 0) || got.Mode().Perm() != 0o600 || !os.SameFile(want, got) || want.Size() != got.Size() || !want.ModTime().Equal(got.ModTime()) {
			return refused("the reserved replacement selected another actual read-cap checkpoint")
		}
	}
	return nil
}

func (b *backend) prepareReservedReplacementV2(ctx context.Context, r Request, record *sessionRecord, roles []*os.File) (Response, error) {
	if err := b.checkReservedSlotRolesV2(record, roles); err != nil {
		return Response{}, err
	}
	metadata := make([]byte, r.MetaSize)
	if _, err := io.ReadFull(os.Stdin, metadata); err != nil {
		return Response{}, err
	}
	if _, err := checkpoint.DecodeMetadataV1(metadata); err != nil {
		return Response{}, err
	}
	check := func() error {
		return errors.Join(ctx.Err(), b.checkReservedSlotRolesV2(record, roles), requireOriginalSlotActorsExitedV2(roles))
	}
	if err := b.createCheckpointV1(ctx, r.Session, r.Safety, roles[9], roles[16], metadata, 0, check); err != nil {
		return Response{}, err
	}
	name := "vms/" + r.Session + "/checkpoints/.rewind-" + r.Generation + ".part"
	replacement, err := b.open(name, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR, 0o600)
	if err != nil {
		return Response{}, err
	}
	if err := unix.IoctlFileClone(int(replacement.Fd()), int(roles[18].Fd())); err != nil {
		cleanup := b.removeCapturedFile(name, replacement)
		return Response{}, errors.Join(err, cleanup, replacement.Close())
	}
	if err := errors.Join(replacement.Chown(record.SlotUID, b.caller.gid), replacement.Chmod(0o660)); err != nil {
		cleanup := b.removeCapturedFile(name, replacement)
		return Response{}, errors.Join(err, cleanup, replacement.Close())
	}
	transaction, err := b.replacementTransactionV2(r, roles, replacement, checkpoint.RestorePreparedV1)
	if err != nil {
		cleanup := b.removeCapturedFile(name, replacement)
		return Response{}, errors.Join(err, cleanup, replacement.Close())
	}
	// From this point the actual replacement belongs in the returned native
	// owner even when sync, final checks or the final ACK fail.
	failure := errors.Join(replacement.Sync(), roles[16].Sync(), check())
	return reservedTransactionReplyV2(transaction, failure, replacement)
}

func (b *backend) changeReservedReplacementV2(ctx context.Context, r Request, record *sessionRecord, roles []*os.File) (Response, error) {
	original, replacement := roles[9], roles[20]
	info, err := replacement.Stat()
	if err != nil || !info.Mode().IsRegular() || !owned(info, record.SlotUID) || info.Mode().Perm() != 0o660 {
		return Response{}, refused("the replacement is not its actual root-created slot descriptor")
	}
	part := "vms/" + r.Session + "/checkpoints/.rewind-" + r.Generation + ".part"
	disk := "vms/" + r.Session + "/disk.img"
	oldAtDisk := b.namedSameV2(disk, original)
	newAtDisk := b.namedSameV2(disk, replacement)
	oldAtPart := b.namedSameV2(part, original)
	newAtPart := b.namedSameV2(part, replacement)
	phase := ""
	root, err := b.root.Open(".")
	if err != nil {
		return Response{}, err
	}
	defer root.Close()
	switch r.Op {
	case "checkpoint.exchange-v2":
		if oldAtDisk && newAtPart {
			if err := unix.Renameat2(int(root.Fd()), disk, int(root.Fd()), part, unix.RENAME_EXCHANGE); err != nil {
				return Response{}, err
			}
		} else if !(newAtDisk && oldAtPart) {
			return Response{}, refused("the actual original and replacement named roles changed before exchange")
		}
		phase = checkpoint.RestoreExchangedV1
	case "checkpoint.rollback-v2":
		if newAtDisk && oldAtPart {
			if err := unix.Renameat2(int(root.Fd()), disk, int(root.Fd()), part, unix.RENAME_EXCHANGE); err != nil {
				return Response{}, err
			}
		} else if !(oldAtDisk && (newAtPart || info.Sys().(*syscall.Stat_t).Nlink == 0)) {
			return Response{}, refused("the actual named roles cannot roll back this original replacement")
		}
		phase = checkpoint.RestoreRolledBackV1
		if b.namedSameV2(part, replacement) {
			err = b.removeCapturedFile(part, replacement)
		}
	case "checkpoint.finalize-v2":
		if newAtDisk && oldAtPart {
			err = b.removeCapturedFile(part, original)
		} else if !newAtDisk {
			return Response{}, refused("the original reserved disk has no actual committed replacement role")
		}
		if err != nil {
			return Response{}, err
		}
		phase = checkpoint.RestoreFinalizedV1
	default:
		return Response{}, refused("the reserved replacement phase is unknown")
	}
	// Phase comes from actual inode movement BEFORE any fallible sync/check.
	transaction, err := b.replacementTransactionV2(r, roles, replacement, phase)
	if err != nil {
		return Response{}, err
	}
	failure := errors.Join(err, ctx.Err(), b.syncDir("vms/"+r.Session), roles[16].Sync(), b.checkReservedSlotCoreV2(record, roles), requireOriginalSlotActorsExitedV2(roles))
	return reservedTransactionReplyV2(transaction, failure)
}

func (b *backend) namedSameV2(name string, file *os.File) bool {
	if file == nil {
		return false
	}
	current, err := b.root.Lstat(name)
	if err != nil || !current.Mode().IsRegular() {
		return false
	}
	held, err := file.Stat()
	return err == nil && os.SameFile(current, held)
}

func (b *backend) replacementTransactionV2(r Request, roles []*os.File, replacement *os.File, phase string) (checkpoint.TransactionV1, error) {
	selectedMeta, err := readCheckpointMetadataFDV2(roles[19])
	if err != nil {
		return checkpoint.TransactionV1{}, err
	}
	safetyFiles, err := b.selectCheckpointV1(r.Session, r.Safety, roles[16])
	if err != nil {
		return checkpoint.TransactionV1{}, err
	}
	defer func() {
		for _, file := range safetyFiles {
			file.Close()
		}
	}()
	safetyMeta, err := readCheckpointMetadataFDV2(safetyFiles[1])
	if err != nil {
		return checkpoint.TransactionV1{}, err
	}
	selectedTime, _ := time.Parse(checkpoint.TimeLayout, r.Name)
	safetyTime, _ := time.Parse(checkpoint.TimeLayout, r.Safety)
	originalRef, err := describeSlotDiskV2(roles[9])
	if err != nil {
		return checkpoint.TransactionV1{}, err
	}
	replacementRef, err := describeSlotDiskV2(replacement)
	if err != nil {
		return checkpoint.TransactionV1{}, err
	}
	originalName, replacementName := "disk.img", ".rewind-"+r.Generation+".part"
	if b.namedSameV2("vms/"+r.Session+"/disk.img", replacement) {
		originalName, replacementName = replacementName, originalName
	}
	return checkpoint.TransactionV1{Version: 1, Generation: r.Generation, Phase: phase, Selected: checkpoint.Entry{Time: selectedTime, Metadata: selectedMeta}, Safety: checkpoint.Entry{Time: safetyTime, Metadata: safetyMeta}, Original: checkpoint.DiskRoleV1{Role: "original", Name: originalName, Reference: originalRef}, Replacement: checkpoint.DiskRoleV1{Role: "replacement", Name: replacementName, Reference: replacementRef}}, nil
}

func readCheckpointMetadataFDV2(file *os.File) (checkpoint.Metadata, error) {
	data, err := io.ReadAll(io.NewSectionReader(file, 0, (64<<10)+1))
	if err != nil {
		return checkpoint.Metadata{}, err
	}
	return checkpoint.DecodeMetadataV1(data)
}

// This is journal DATA describing actual held native objects. No consumer
// may use its names/references to reconstruct a Source or replacement owner.
func describeSlotDiskV2(file *os.File) (cachefs.Reference, error) {
	info, err := file.Stat()
	if err != nil {
		return cachefs.Reference{}, err
	}
	if !info.Mode().IsRegular() {
		return cachefs.Reference{}, refused("a reserved journal disk role is not a regular native file")
	}
	native := info.Sys().(*syscall.Stat_t)
	return cachefs.Reference{Version: 1, Identity: fmt.Sprintf("slot-v2:%x:%x:%d", native.Dev, native.Ino, native.Uid), Mode: uint32(info.Mode().Perm()), Size: info.Size(), Modified: info.ModTime()}, nil
}

func reservedTransactionReplyV2(transaction checkpoint.TransactionV1, failure error, outputs ...*os.File) (Response, error) {
	data, err := json.Marshal(transaction)
	if err != nil {
		for _, file := range outputs {
			file.Close()
		}
		return Response{}, errors.Join(failure, err)
	}
	response, err := checkpointNativeReplyV1(Response{OK: failure == nil, CheckpointTransaction: data}, outputs...)
	return response, errors.Join(failure, err)
}
