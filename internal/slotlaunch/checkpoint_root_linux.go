//go:build linux

package slotlaunch

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/checkpoint"
	"golang.org/x/sys/unix"
)

// checkpointRunningV1 runs only inside inspectGuest's original native peer
// inspection and retained root session lock. The disk and directory roles are
// actual SCM_RIGHTS inputs; a session name alone cannot select the source.
func (b *backend) checkpointRunningV1(ctx context.Context, r Request, record *sessionRecord, allocation slotRecord, disk, keeper, init, peer, directory *os.File) (Response, error) {
	if err := b.checkCheckpointSourceV1(ctx, record, allocation, disk, keeper, init, peer); err != nil {
		return Response{}, err
	}
	if r.Action == "open" {
		directory, err := b.openCheckpointDirectoryV1(record.Session)
		if err != nil {
			return Response{}, err
		}
		return checkpointNativeReplyV1(Response{OK: true}, directory)
	}
	if err := b.checkCheckpointDirectoryV1(record.Session, directory); err != nil {
		return Response{}, err
	}
	response := Response{OK: true}
	var outputs []*os.File
	var err error
	switch r.Action {
	case "probe":
		err = b.probeCheckpointV1(record.Session, disk, directory)
	case "list":
		response.Checkpoints, err = b.listCheckpointsV1(record.Session, directory)
	case "trim":
		err = b.trimCheckpointsV1(record.Session, directory, r.KeepCount)
	case "create":
		metadata := make([]byte, r.MetaSize)
		if _, err = io.ReadFull(os.Stdin, metadata); err == nil {
			err = b.createCheckpointV1(ctx, record.Session, r.Name, disk, directory, metadata, r.KeepCount, func() error { return b.checkCheckpointSourceV1(ctx, record, allocation, disk, keeper, init, peer) }, func(copy, meta *os.File) error {
				return b.loanCreatedCheckpointV2(record.Session, r.Name, directory, copy, meta)
			})
		}
	case "select":
		outputs, err = b.selectCheckpointV1(record.Session, r.Name, directory)
	default:
		err = refused("the live checkpoint action is unknown")
	}
	if err == nil {
		err = b.checkCheckpointSourceV1(ctx, record, allocation, disk, keeper, init, peer)
	}
	if err == nil {
		err = b.checkCheckpointDirectoryV1(record.Session, directory)
	}
	if err != nil {
		for _, file := range outputs {
			file.Close()
		}
		return Response{}, err
	}
	return checkpointNativeReplyV1(response, outputs...)
}

func (b *backend) checkCheckpointSourceV1(ctx context.Context, record *sessionRecord, allocation slotRecord, disk, keeper, init, peer *os.File) error {
	if ctx == nil || ctx.Err() != nil || record == nil || record.Slot == nil || record.Session != allocation.Session || record.UID != b.caller.uid || record.GID != b.caller.gid {
		return refused("the checkpoint has no original current slot allocation")
	}
	if !pidLive(keeper) || !pidLive(init) || !pidLive(peer) {
		return refused("the original checkpoint keeper, init or VMM ended")
	}
	poll := []unix.PollFd{{Fd: 0, Events: unix.POLLRDHUP}}
	if count, err := unix.Poll(poll, 0); err != nil || count != 0 {
		return refused("the original checkpoint caller closed its native operation")
	}
	keeperPID, err := nativePID(keeper)
	if err != nil {
		return err
	}
	initPID, err := nativePID(init)
	if err != nil {
		return err
	}
	status, err := os.ReadFile("/proc/" + strconv.Itoa(initPID) + "/status")
	if err != nil || len(status) > RequestMax {
		return refused("the original checkpoint init parent cannot be checked")
	}
	parentOK := false
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "PPid:" {
			parentOK = fields[1] == strconv.Itoa(keeperPID)
		}
	}
	if !parentOK {
		return refused("the original checkpoint init has another keeper parent")
	}
	canonical, err := b.open("vms/"+record.Session+"/disk.img", unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer canonical.Close()
	left, err := canonical.Stat()
	if err != nil {
		return err
	}
	right, err := disk.Stat()
	if err != nil || !os.SameFile(left, right) || !left.Mode().IsRegular() || left.Sys().(*syscall.Stat_t).Nlink != 1 || !owned(left, allocation.SlotUID) || left.Mode().Perm() != 0o660 {
		return refused("the checkpoint disk is not the same original root-created slot file")
	}
	return nil
}

func (b *backend) openCheckpointDirectoryV1(session string) (*os.File, error) {
	name := "vms/" + session + "/checkpoints"
	if err := b.root.Mkdir(name, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	file, err := b.open(name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	if err := b.checkCheckpointDirectoryV1(session, file); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func (b *backend) checkCheckpointDirectoryV1(session string, directory *os.File) error {
	if directory == nil {
		return refused("the checkpoint has no original root directory descriptor")
	}
	got, err := directory.Stat()
	if err != nil || !got.IsDir() || !owned(got, 0) || got.Mode().Perm() != 0o700 {
		return refused("the checkpoint directory is not protected by its original root creator")
	}
	want, err := b.root.Lstat("vms/" + session + "/checkpoints")
	if err != nil || want.Mode()&os.ModeSymlink != 0 || !os.SameFile(got, want) {
		return refused("the original root checkpoint directory changed identity")
	}
	return nil
}

func (b *backend) probeCheckpointV1(session string, disk, directory *os.File) error {
	name, err := temporary("vms/" + session + "/checkpoints/.probe")
	if err != nil {
		return err
	}
	file, err := b.open(name, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = b.removeCapturedFile(name, file); _ = file.Close() }()
	if err := unix.IoctlFileClone(int(file.Fd()), int(disk.Fd())); err != nil {
		if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EXDEV) || errors.Is(err, unix.EINVAL) {
			return &launchError{Code: "rewind_off", Message: "checkpoints are off: the original slot disk filesystem cannot clone files", Hint: "Use a microvm on a filesystem whose FICLONE probe succeeds."}
		}
		return err
	}
	return b.checkCheckpointDirectoryV1(session, directory)
}

func (b *backend) listCheckpointsV1(session string, directory *os.File) ([]string, error) {
	if err := b.checkCheckpointDirectoryV1(session, directory); err != nil {
		return nil, err
	}
	if _, err := directory.Seek(0, 0); err != nil {
		return nil, err
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	if len(entries) > 4096 {
		return nil, refused("the checkpoint directory exceeds its entry bound")
	}
	var result []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		when, err := time.Parse(checkpoint.TimeLayout, name)
		if err != nil || when.Format(checkpoint.TimeLayout) != name {
			continue
		}
		files, err := b.selectCheckpointV1(session, name, directory)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			file.Close()
		}
		result = append(result, name)
	}
	slices.Sort(result)
	slices.Reverse(result)
	return result, nil
}

func (b *backend) selectCheckpointV1(session, name string, directory *os.File) ([]*os.File, error) {
	if err := b.checkCheckpointDirectoryV1(session, directory); err != nil {
		return nil, err
	}
	var result []*os.File
	for _, suffix := range []string{".disk", ".json"} {
		file, err := b.open("vms/"+session+"/checkpoints/"+name+suffix, unix.O_RDONLY, 0)
		if err != nil {
			for _, previous := range result {
				previous.Close()
			}
			return nil, err
		}
		st, err := file.Stat()
		if err != nil || !singleRegular(st, 0) || st.Mode().Perm() != 0o600 || suffix == ".json" && st.Size() > 64<<10 {
			file.Close()
			for _, previous := range result {
				previous.Close()
			}
			return nil, refused("a selected checkpoint is not its original protected root file")
		}
		result = append(result, file)
	}
	return result, nil
}

func (b *backend) createCheckpointV1(ctx context.Context, session, name string, disk, directory *os.File, metadata []byte, keep int, check func() error, created ...func(*os.File, *os.File) error) (returned error) {
	if _, err := checkpoint.DecodeMetadataV1(metadata); err != nil {
		return err
	}
	prefix := "vms/" + session + "/checkpoints/"
	for _, suffix := range []string{".disk", ".json"} {
		if _, err := b.root.Lstat(prefix + name + suffix); !errors.Is(err, os.ErrNotExist) {
			return refused("the checkpoint timestamp is already in use")
		}
	}
	part, err := temporary(prefix + ".checkpoint")
	if err != nil {
		return err
	}
	copy, err := b.open(part, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		leaf := part
		if published {
			leaf = prefix + name + ".disk"
		}
		if returned != nil || !published {
			returned = errors.Join(returned, b.removeCapturedFile(leaf, copy))
		}
		returned = errors.Join(returned, copy.Close())
	}()
	if err := unix.IoctlFileClone(int(copy.Fd()), int(disk.Fd())); err != nil {
		return err
	}
	if err := errors.Join(ctx.Err(), copy.Sync(), b.checkCheckpointDirectoryV1(session, directory), check()); err != nil {
		return err
	}
	root, err := b.root.Open(".")
	if err != nil {
		return err
	}
	defer root.Close()
	if err := unix.Renameat2(int(root.Fd()), part, int(root.Fd()), prefix+name+".disk", unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	published = true
	meta, err := b.open(prefix+name+".json", unix.O_CREAT|unix.O_EXCL|unix.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	metaOK := false
	defer func() {
		if !metaOK || returned != nil {
			returned = errors.Join(returned, b.removeCapturedFile(prefix+name+".json", meta))
		}
		returned = errors.Join(returned, meta.Close())
	}()
	if _, err := meta.Write(metadata); err != nil {
		return err
	}
	if err := errors.Join(meta.Sync(), directory.Sync(), ctx.Err(), check()); err != nil {
		return err
	}
	if len(created) > 0 {
		if err := created[0](copy, meta); err != nil {
			return err
		}
		if err := check(); err != nil {
			return err
		}
	}
	metaOK = true
	if keep > 0 {
		return b.trimCheckpointsV1(session, directory, keep)
	}
	return nil
}

func (b *backend) loanCreatedCheckpointV2(session, name string, directory, disk, metadata *os.File) error {
	outputs, err := b.selectCheckpointV1(session, name, directory)
	if err != nil {
		return err
	}
	for index, want := range []*os.File{disk, metadata} {
		left, err := want.Stat()
		if err != nil {
			for _, file := range outputs {
				file.Close()
			}
			return err
		}
		right, err := outputs[index].Stat()
		if err != nil || !os.SameFile(left, right) {
			for _, file := range outputs {
				file.Close()
			}
			return refused("the created checkpoint loan lost its actual creator pair")
		}
	}
	response, err := checkpointNativeReplyV1(Response{OK: true, CheckpointStage: "created"}, outputs...)
	if err != nil {
		return err
	}
	err = sendNativeResponse(response)
	response.native.close()
	return err
}

func (b *backend) trimCheckpointsV1(session string, directory *os.File, keep int) error {
	if keep < 0 || keep > 1024 {
		return refused("the checkpoint retention count is outside its bound")
	}
	names, err := b.listCheckpointsV1(session, directory)
	if err != nil {
		return err
	}
	for _, name := range names[min(keep, len(names)):] {
		files, err := b.selectCheckpointV1(session, name, directory)
		if err != nil {
			return err
		}
		for i, suffix := range []string{".disk", ".json"} {
			if err := b.removeCapturedFile("vms/"+session+"/checkpoints/"+name+suffix, files[i]); err != nil {
				for _, file := range files {
					file.Close()
				}
				return err
			}
		}
		for _, file := range files {
			file.Close()
		}
	}
	return directory.Sync()
}

func checkpointNativeReplyV1(response Response, outputs ...*os.File) (Response, error) {
	self, err := os.Open("/proc/self/exe")
	if err != nil {
		for _, file := range outputs {
			file.Close()
		}
		return Response{}, err
	}
	sender, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		self.Close()
		for _, file := range outputs {
			file.Close()
		}
		return Response{}, err
	}
	response.native = &nativeTransfer{files: append(outputs, self, os.NewFile(uintptr(sender), "original-checkpoint-root-sender"))}
	return response, nil
}
