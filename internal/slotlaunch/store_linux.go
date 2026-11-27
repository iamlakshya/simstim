//go:build linux

package slotlaunch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

type sessionRecord struct {
	Session string   `json:"session"`
	UID     int      `json:"uid"`
	GID     int      `json:"gid"`
	Digests []string `json:"digests"`
	Slot    *int     `json:"slot,omitempty"`
	SlotUID int      `json:"slot_uid,omitempty"`
}

func (b *backend) store(r Request, input io.Reader) error {
	lock, err := b.lock("store/.lock", false)
	if err != nil {
		return err
	}
	defer lock.Close()
	name := "store/sha256/" + r.SHA256
	if existing, err := b.open(name, unix.O_RDONLY, 0); err == nil {
		defer existing.Close()
		if err := b.checkStore(existing, r.SHA256, r.Size); err != nil {
			return err
		}
		// Consume and check the payload even when the store already has it.
		return checkPayload(io.Discard, input, r.Size, r.SHA256)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp, err := temporary("store/incoming/file")
	if err != nil {
		return err
	}
	file, err := b.open(tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer b.root.Remove(tmp)
	err = checkPayload(file, input, r.Size, r.SHA256)
	if err == nil {
		err = file.Chmod(0o444)
	}
	if err == nil {
		err = file.Sync()
	}
	err = joinClose(err, file)
	if err != nil {
		return err
	}
	if err := b.root.Rename(tmp, name); err != nil {
		return err
	}
	return b.syncDir("store/sha256")
}

// checkPayload reads exactly the declared bytes. It does not consume the next
// protocol object, and an early EOF or checksum mismatch never publishes.
func checkPayload(output io.Writer, input io.Reader, size int64, digest string) error {
	hash := sha256.New()
	n, err := io.CopyN(io.MultiWriter(output, hash), input, size)
	if err != nil || n != size {
		return refused("the launcher payload ended before its declared size")
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return refused("the launcher payload does not match its SHA-256")
	}
	return nil
}

func (b *backend) checkStore(file *os.File, digest string, size int64) error {
	st, err := file.Stat()
	if err != nil || !singleRegular(st, b.owner) || st.Mode().Perm()&0o222 != 0 {
		return refused("a store object is writable or is not a root-owned single-link regular file")
	}
	if size > 0 && st.Size() != size {
		return refused("a store object has the wrong size")
	}
	if st.Size() < 1 || st.Size() > artifactMax {
		return refused("a store object has an invalid size")
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if err := checkPayload(io.Discard, file, st.Size(), digest); err != nil {
		return err
	}
	_, err = file.Seek(0, 0)
	return err
}

func (b *backend) session(id string, create bool) (*sessionRecord, *os.File, error) {
	lock, err := b.lock("sessions/"+id+".lock", false)
	if err != nil {
		return nil, nil, err
	}
	var record sessionRecord
	err = b.readJSON("sessions/"+id+".json", &record)
	if errors.Is(err, os.ErrNotExist) && create {
		return &sessionRecord{Session: id, UID: b.caller.uid, GID: b.caller.gid, Digests: []string{}}, lock, nil
	}
	if err != nil {
		lock.Close()
		return nil, nil, err
	}
	if record.Session != id || record.UID != b.caller.uid || record.GID != b.caller.gid {
		lock.Close()
		return nil, nil, refused("this session belongs to another login account")
	}
	for _, digest := range record.Digests {
		if !supply.IsHex64(digest) {
			lock.Close()
			return nil, nil, refused("the protected session record contains an invalid digest")
		}
	}
	if record.Slot != nil && (*record.Slot < 0 || *record.Slot >= SlotCount) {
		lock.Close()
		return nil, nil, refused("the protected session record contains an invalid slot")
	}
	return &record, lock, nil
}

func (b *backend) prune(keep []string) ([]string, error) {
	lock, err := b.lock("store/.lock", false)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	defer func() { _ = b.refreshCapacityLocked() }()
	retained := map[string]bool{}
	for _, digest := range keep {
		retained[digest] = true
	}
	folder, err := b.open("sessions", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	entries, err := folder.ReadDir(-1)
	folder.Close()
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !api.ValidSessionID(id) {
			return nil, refused("the session store contains an unknown record")
		}
		var record sessionRecord
		if err := b.readJSON("sessions/"+entry.Name(), &record); err != nil {
			return nil, err
		}
		if record.Session != id || record.UID <= 0 {
			return nil, refused("a retained session record has an invalid owner")
		}
		for _, digest := range record.Digests {
			if !supply.IsHex64(digest) {
				return nil, refused("a retained session record has an invalid digest")
			}
			retained[digest] = true
		}
	}
	folder, err = b.open("store/sha256", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	entries, err = folder.ReadDir(-1)
	folder.Close()
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, entry := range entries {
		name := entry.Name()
		if !supply.IsHex64(name) || retained[name] {
			continue
		}
		file, err := b.open("store/sha256/"+name, unix.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		st, err := file.Stat()
		file.Close()
		if err != nil {
			return nil, err
		}
		if !singleRegular(st, b.owner) || st.Mode().Perm()&0o222 != 0 {
			return nil, refused("the store contains an unsafe object")
		}
		if time.Since(st.ModTime()) <= time.Hour {
			continue
		}
		if err := b.root.Remove("store/sha256/" + name); err != nil {
			return nil, err
		}
		removed = append(removed, name)
	}
	slices.Sort(removed)
	if err := b.expireIncoming(); err != nil {
		return nil, err
	}
	return removed, b.syncDir("store/sha256")
}

func (b *backend) expireIncoming() error {
	folder, err := b.open("store/incoming", unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	entries, err := folder.ReadDir(-1)
	folder.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		suffix := strings.TrimPrefix(name, "file.")
		if suffix == name || len(suffix) != 32 || !supply.IsHex64(suffix+suffix) {
			continue
		}
		file, err := b.open("store/incoming/"+name, unix.O_RDONLY, 0)
		if err != nil {
			return err
		}
		st, err := file.Stat()
		file.Close()
		if err != nil || !singleRegular(st, b.owner) || st.Mode().Perm() != 0o600 {
			return refused("an unfinished store upload has an unsafe native object")
		}
		if time.Since(st.ModTime()) <= time.Hour {
			continue
		}
		if err := b.root.Remove("store/incoming/" + name); err != nil {
			return err
		}
	}
	return b.syncDir("store/incoming")
}

func (b *backend) copyDisk(from, to string) error {
	fromID, fromName, _ := diskName(from)
	toID, toName, _ := diskName(to)
	ids := []string{fromID}
	if toID != fromID {
		ids = append(ids, toID)
	}
	slices.Sort(ids)
	var locks []*os.File
	defer func() {
		for _, lock := range locks {
			lock.Close()
		}
	}()
	var destination *sessionRecord
	var origin *sessionRecord
	for _, id := range ids {
		record, lock, err := b.session(id, id == toID)
		if err != nil {
			return err
		}
		locks = append(locks, lock)
		if id == toID {
			destination = record
		}
		if id == fromID {
			origin = record
		}
	}
	// A live source can make an atomic reflink while its original guest
	// operation is frozen. A live destination is writable only when the
	// destination is a new checkpoint rather than its running disk.
	liveSource := b.stopped(fromID) != nil
	if toID != fromID || toName == "disk.img" {
		if err := b.stopped(toID); err != nil {
			return err
		}
	}
	source, err := b.open("vms/"+fromID+"/"+fromName, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer source.Close()
	st, err := source.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Sys().(*syscall.Stat_t).Nlink != 1 || !owned(st, b.caller.uid) && !owned(st, origin.SlotUID) {
		return refused("the source disk is not a regular file")
	}
	if toID != fromID || !liveSource {
		if err := b.ensureVMDir(toID, b.caller.uid, b.caller.gid); err != nil {
			return err
		}
	}
	if strings.HasPrefix(toName, "checkpoints/") {
		owner := b.caller.uid
		if toID == fromID && liveSource && origin.SlotUID > 0 {
			owner = origin.SlotUID
		}
		if err := b.mkdir("vms/"+toID+"/checkpoints", 0o770, owner, b.caller.gid); err != nil {
			return err
		}
	}
	target := "vms/" + toID + "/" + toName
	tmp, err := temporary(target)
	if err != nil {
		return err
	}
	output, err := b.open(tmp, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0o660)
	if err != nil {
		return err
	}
	defer b.root.Remove(tmp)
	err = unix.IoctlFileClone(int(output.Fd()), int(source.Fd()))
	if err != nil {
		if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOTTY) && !errors.Is(err, unix.EXDEV) && !errors.Is(err, unix.EINVAL) {
			output.Close()
			return err
		}
		if liveSource {
			output.Close()
			return refused("a live session disk needs an atomic reflink; stop it before a sparse copy")
		}
		err = sparseCopy(source, output, st.Size())
	}
	if err == nil {
		err = output.Chown(b.caller.uid, b.caller.gid)
	}
	if err == nil {
		err = output.Sync()
	}
	err = joinClose(err, output)
	if err != nil {
		return err
	}
	if err := b.root.Rename(tmp, target); err != nil {
		return err
	}
	if len(destination.Digests) == 0 {
		destination.Digests = slices.Clone(origin.Digests)
	}
	if err := b.writeJSON("sessions/"+toID+".json", destination); err != nil {
		return err
	}
	return b.syncDir(filepath.Dir(target))
}

func sparseCopy(source, target *os.File, size int64) error {
	if err := target.Truncate(size); err != nil {
		return err
	}
	for offset := int64(0); offset < size; {
		data, err := unix.Seek(int(source.Fd()), offset, unix.SEEK_DATA)
		if errors.Is(err, unix.ENXIO) {
			return nil
		}
		if errors.Is(err, unix.EINVAL) {
			if _, err := source.Seek(0, 0); err != nil {
				return err
			}
			if _, err := target.Seek(0, 0); err != nil {
				return err
			}
			_, err = io.CopyN(target, source, size)
			return err
		}
		if err != nil {
			return err
		}
		hole, err := unix.Seek(int(source.Fd()), data, unix.SEEK_HOLE)
		if err != nil {
			return err
		}
		hole = min(hole, size)
		if _, err := source.Seek(data, 0); err != nil {
			return err
		}
		if _, err := target.Seek(data, 0); err != nil {
			return err
		}
		if _, err := io.CopyN(target, source, hole-data); err != nil {
			return err
		}
		offset = hole
	}
	return nil
}

func (b *backend) mkdir(name string, mode os.FileMode, uid, gid int) error {
	if err := b.root.Mkdir(name, mode); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	file, err := b.open(name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chown(uid, gid); err != nil {
		return err
	}
	return file.Chmod(mode)
}

func (b *backend) ensureVMDir(id string, uid, gid int) error {
	return b.mkdir("vms/"+id, os.ModeSetgid|0o770, uid, gid)
}

func (b *backend) stopped(id string) error {
	var record sessionRecord
	if err := b.readJSON("sessions/"+id+".json", &record); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if record.Slot == nil {
		return nil
	}
	var allocation slotRecord
	if err := b.readJSON(slotRecordName(*record.Slot), &allocation); err != nil {
		return err
	}
	if allocation.Session != id {
		return nil
	} // A later root allocation settled this old lease before reuse.
	lock, err := b.lock(fmt.Sprintf("slots/%02d.lock", *record.Slot), true)
	if err != nil {
		return refused("the session still owns a live or uncertain slot")
	}
	defer lock.Close()
	return b.vacancy(*record.Slot)
}
