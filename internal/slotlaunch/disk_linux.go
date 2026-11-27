//go:build linux

package slotlaunch

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"syscall"
)

func (b *backend) prepareVM(r Request, uid int) (directory, disk *os.File, fresh bool, returned error) {
	if err := b.ensureVMDir(r.Session, uid, b.caller.gid); err != nil {
		return nil, nil, false, err
	}
	dir, err := b.open("vms/"+r.Session, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, nil, false, err
	}
	defer func() {
		if returned != nil {
			dir.Close()
		}
	}()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, nil, false, err
	}
	for _, entry := range entries {
		name := "vms/" + r.Session + "/" + entry.Name()
		if entry.IsDir() {
			if entry.Name() != "checkpoints" {
				return nil, nil, false, refused("the session folder contains an unexpected directory")
			}
			continue
		}
		if entry.Type()&os.ModeSocket != 0 {
			if entry.Name() != "api.sock" && entry.Name() != "vsock.sock" {
				return nil, nil, false, refused("the session folder contains an unexpected socket")
			}
			if err := b.root.Remove(name); err != nil {
				return nil, nil, false, err
			}
			continue
		}
		if entry.Name() == "firecracker.json" {
			if err := b.root.Remove(name); err != nil {
				return nil, nil, false, err
			}
			continue
		}
		file, err := b.open(name, unix.O_RDONLY, 0)
		if err != nil {
			return nil, nil, false, err
		}
		st, err := file.Stat()
		if err != nil || !st.Mode().IsRegular() {
			file.Close()
			return nil, nil, false, refused("the session folder contains a non-regular file")
		}
		stat := st.Sys().(*syscall.Stat_t)
		if stat.Nlink != 1 {
			file.Close()
			return nil, nil, false, refused("the session folder contains a hard-linked file")
		}
		err = file.Chown(uid, b.caller.gid)
		file.Close()
		if err != nil {
			return nil, nil, false, err
		}
	}
	name := "vms/" + r.Session + "/disk.img"
	file, err := b.open(name, unix.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		name, err = temporary("vms/" + r.Session + "/.new-disk")
		if err != nil {
			return nil, nil, false, err
		}
		file, err = b.open(name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL, 0o660)
		fresh = err == nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	defer func() {
		if returned != nil {
			file.Close()
			if fresh {
				_ = b.root.Remove(name)
			}
		}
	}()
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Sys().(*syscall.Stat_t).Nlink != 1 {
		return nil, nil, false, refused("the session disk is not a single-link regular file")
	}
	if fresh {
		if err := file.Truncate(r.DiskMaxGiB * (1 << 30)); err != nil {
			return nil, nil, false, err
		}
	} else if st.Size() != r.DiskMaxGiB*(1<<30) {
		return nil, nil, false, refused("the existing session disk differs from its admitted limit")
	}
	if err := file.Chown(uid, b.caller.gid); err != nil {
		return nil, nil, false, err
	}
	if err := file.Chmod(0o660); err != nil {
		return nil, nil, false, err
	}
	return dir, file, fresh, nil
}

func (b *backend) removeCapturedFile(name string, file *os.File) error {
	before, err := file.Stat()
	if err != nil {
		return err
	}
	current, err := b.root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(before, current) {
		return refused("a temporary disk pathname changed; its replacement was retained")
	}
	return b.root.Remove(name)
}

func (b *backend) publishFreshDisk(id string, file *os.File) error {
	before, err := file.Stat()
	if err != nil {
		return err
	}
	current, err := b.root.Lstat(file.Name())
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(before, current) {
		return refused("the captured fresh disk changed before publication")
	}
	root, err := b.root.Open(".")
	if err != nil {
		return err
	}
	defer root.Close()
	name := "vms/" + id + "/disk.img"
	if err := unix.Renameat2(int(root.Fd()), file.Name(), int(root.Fd()), name, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	current, err = b.root.Lstat(name)
	if err != nil || !os.SameFile(before, current) {
		return refused("the published disk differs from the original captured fresh disk")
	}
	return b.syncDir("vms/" + id)
}
