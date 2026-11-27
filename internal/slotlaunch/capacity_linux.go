//go:build linux

package slotlaunch

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// CapacityNativeV1 only reads the fixed advisory projection. It starts no
// process, acquires no reservation and supplies no account or execution proof.
func CapacityNativeV1() (CapacityProjectionV1, bool) {
	uid := os.Getuid()
	if !validAccountID(uid) {
		return CapacityProjectionV1{}, false
	}
	boot, err := nativeBootID()
	if err != nil {
		return CapacityProjectionV1{}, false
	}
	root, err := openOwnedRoot(RootPath(), 0)
	if err != nil {
		return CapacityProjectionV1{}, false
	}
	defer root.Close()
	b := &backend{root: root, owner: 0}
	return b.readCapacity(uint32(uid), boot, time.Now().UTC())
}

func (b *backend) readCapacity(uid uint32, boot string, now time.Time) (CapacityProjectionV1, bool) {
	dir, err := b.capacityDirectory()
	if err != nil {
		return CapacityProjectionV1{}, false
	}
	dir.Close()
	file, err := b.open(CapacityProjectionRelativeV1(uid), unix.O_RDONLY, 0)
	if err != nil {
		return CapacityProjectionV1{}, false
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil || !singleRegular(st, b.owner) || st.Mode().Perm() != 0o644 || st.Size() < 1 || st.Size() > CapacityProjectionMaxV1 {
		return CapacityProjectionV1{}, false
	}
	raw, err := io.ReadAll(io.LimitReader(file, CapacityProjectionMaxV1+1))
	if err != nil {
		return CapacityProjectionV1{}, false
	}
	return parseCapacityProjectionV1(raw, uid, boot, now)
}

// refreshCapacityLocked runs after the invocation releases its local slot
// lease and before releasing the existing store lock. Native effects do not
// depend on this advisory write. A failed inventory publishes unknown counts.
func (b *backend) refreshCapacityLocked() error {
	boot, err := nativeBootID()
	if err != nil {
		return err
	}
	generation, err := temporary("capacity")
	if err != nil {
		return err
	}
	record := CapacityProjectionV1{UID: uint32(b.caller.uid), BootID: boot,
		Generation: strings.TrimPrefix(generation, "capacity."), ObservedUnix: time.Now().UTC().Unix()}
	if inventory, err := b.slotInventory(); err == nil {
		counts := capacityCountsFromSlots(inventory)
		if validCapacityCountsV1(counts) {
			record.Counts = &counts
		}
	}
	return b.writeCapacityLocked(record)
}

func capacityCountsFromSlots(inventory []Slot) CapacityCountsV1 {
	counts := CapacityCountsV1{Total: len(inventory)}
	for i, slot := range inventory {
		if slot.Number != i || slot.Free && slot.Quarantined {
			return CapacityCountsV1{}
		}
		switch {
		case slot.Quarantined:
			counts.Quarantined++
		case slot.Free:
			counts.Free++
		default:
			counts.Occupied++
		}
	}
	return counts
}

func (b *backend) writeCapacityLocked(record CapacityProjectionV1) error {
	if !validAccountID(b.caller.uid) || record.UID != uint32(b.caller.uid) || !capacityBootV1.MatchString(record.BootID) || !capacityGenerationV1.MatchString(record.Generation) || record.ObservedUnix <= 0 || record.Counts != nil && !validCapacityCountsV1(*record.Counts) {
		return refused("the advisory slot projection has invalid native fields")
	}
	name := CapacityProjectionRelativeV1(record.UID)
	dir, err := b.capacityDirectory()
	if errors.Is(err, os.ErrNotExist) {
		// Existing installations may predate this derived projection. Only
		// the final metadata directory is created, under checked ancestors.
		if err := b.root.Mkdir(filepath.Dir(name), 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		dir, err = b.capacityDirectory()
		if err == nil {
			err = dir.Chmod(0o755)
		}
	}
	if err != nil {
		if dir != nil {
			dir.Close()
		}
		return err
	}
	dir.Close()
	if existing, err := b.open(name, unix.O_RDONLY, 0); err == nil {
		st, err := existing.Stat()
		existing.Close()
		if err != nil || !singleRegular(st, b.owner) || st.Mode().Perm() != 0o644 {
			return refused("the existing advisory slot projection is not protected")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw)+1 > CapacityProjectionMaxV1 {
		return refused("the advisory slot projection exceeds its bound")
	}
	tmp, err := temporary(name)
	if err != nil {
		return err
	}
	file, err := b.open(tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer b.root.Remove(tmp)
	_, err = file.Write(append(raw, '\n'))
	if err == nil {
		err = file.Chmod(0o644)
	}
	if err == nil {
		err = file.Sync()
	}
	if err := joinClose(err, file); err != nil {
		return err
	}
	if err := b.root.Rename(tmp, name); err != nil {
		return err
	}
	return b.syncDir(filepath.Dir(name))
}

func (b *backend) capacityDirectory() (*os.File, error) {
	var directory *os.File
	for _, name := range []string{"root-steps", "root-steps/machine", "root-steps/machine/slots", "root-steps/machine/slots/capacity"} {
		if directory != nil {
			directory.Close()
		}
		file, err := b.open(name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			return nil, err
		}
		st, err := file.Stat()
		if err != nil || !owned(st, b.owner) || st.Mode().Perm()&0o022 != 0 {
			file.Close()
			return nil, refused("an advisory slot metadata ancestor is not protected")
		}
		directory = file
	}
	return directory, nil
}
