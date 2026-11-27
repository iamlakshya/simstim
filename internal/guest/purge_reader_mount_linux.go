//go:build linux

package guest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const purgeReaderCopySerialV1 = "reader-copy-v1"
const purgeReaderScratchSerialV1 = "reader-scratch-v1"

func purgeReaderNetworkDevicesV1(names []string) error {
	if len(names) != 1 || names[0] != "lo" {
		return purgeWorkErrorV1("the fixed reader cannot contain a network interface")
	}
	return nil
}

func verifyPurgeReaderNetworkV1() error {
	root, err := os.Open("/sys/class/net")
	if err != nil {
		return err
	}
	names, readErr := root.Readdirnames(2)
	if errors.Is(readErr, io.EOF) {
		readErr = nil
	}
	return errors.Join(readErr, root.Close(), purgeReaderNetworkDevicesV1(names))
}

func verifyPurgeReaderDisksV1(read func(string) (string, error)) error {
	for _, disk := range []struct{ name, serial, readonly string }{
		{"vda", "tools", "1"}, {"vdb", "image", "1"},
		{"vdc", purgeReaderCopySerialV1, "1"}, {"vdd", purgeReaderScratchSerialV1, "0"},
	} {
		serial, err := read("/sys/block/" + disk.name + "/serial")
		if err != nil || serial != disk.serial {
			return purgeWorkErrorV1("the fixed reader has another original disk role or order")
		}
		readonly, err := read("/sys/block/" + disk.name + "/ro")
		if err != nil || readonly != disk.readonly {
			return purgeWorkErrorV1("the fixed reader disk write roles changed")
		}
	}
	return nil
}

// Record the actual native pivot before chdir or old-root teardown can fail.
// A partial reader init must retain that mutation, never close as unpivoted.
func (v *vmFiles) pivotPurgeReaderV1() error {
	root, err := os.OpenRoot(v.root)
	if err != nil {
		return err
	}
	if err := vmDirectory(root, ".oldroot", 0o700); err != nil {
		return errors.Join(err, root.Close())
	}
	if err := root.Close(); err != nil {
		return err
	}
	if err := v.ops.pivot(v.root, filepath.Join(v.root, ".oldroot")); err != nil {
		return err
	}
	v.pivoted = true
	if err := v.ops.chdir("/"); err != nil {
		return err
	}
	if err := v.ops.unmount("/.oldroot", unix.MNT_DETACH); err != nil {
		return err
	}
	return os.Remove("/.oldroot")
}

// This layout consumes only the different reader block roles. It never calls
// mountDisks, overlay, persistentHome, accounts, sessionFreeze or VM prepare.
func (v *vmFiles) layoutPurgeReaderV1() (returned error) {
	for _, disk := range []struct {
		device, target, options string
		flags                   uintptr
	}{
		{"dm-1", "mnt/image", "noload", unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV},
		{"vdc", "mnt/reader-copy", "noload", unix.MS_RDONLY | unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
		{"vdd", "mnt/reader-scratch", "", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC},
	} {
		if err := v.mount(v.boot("dev/"+disk.device), v.boot(disk.target), "ext4", disk.flags, disk.options); err != nil {
			return err
		}
	}
	copyRoot, err := os.OpenRoot(v.boot("mnt/reader-copy"))
	if err != nil {
		return err
	}
	info, statErr := copyRoot.Lstat("work")
	closeErr := copyRoot.Close()
	if statErr != nil || closeErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.Join(purgeWorkErrorV1("the immutable reader copy has no original work directory"), statErr, closeErr)
	}
	scratch, err := os.OpenRoot(v.boot("mnt/reader-scratch"))
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(v.boot("mnt/reader-scratch"))
	if err != nil {
		return errors.Join(err, scratch.Close())
	}
	for _, entry := range entries {
		if entry.Name() != "lost+found" || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return errors.Join(purgeWorkErrorV1("the private reader scratch is not newly empty"), scratch.Close())
		}
		contents, err := os.ReadDir(v.boot("mnt/reader-scratch/lost+found"))
		if err != nil || len(contents) != 0 {
			return errors.Join(purgeWorkErrorV1("the private reader scratch contains prior filesystem data"), err, scratch.Close())
		}
	}
	for _, name := range []string{"overlay", "overlay/upper", "overlay/work"} {
		if err := v.ownedDirectory(scratch, name, 0o700, 0, 0); err != nil {
			return errors.Join(err, scratch.Close())
		}
	}
	if err := scratch.Close(); err != nil {
		return err
	}
	upper := v.boot("mnt/reader-scratch/overlay")
	options := "lowerdir=" + v.boot("mnt/image") + ",upperdir=" + filepath.Join(upper, "upper") + ",workdir=" + filepath.Join(upper, "work")
	if err := v.mount("overlay", v.root, "overlay", unix.MS_NOSUID|unix.MS_NODEV, options); err != nil {
		return err
	}
	root, err := os.OpenRoot(v.root)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, root.Close()) }()
	for _, name := range []string{".", "work", "home", "tmp", "var", "var/tmp", "run", "dev", "proc", "sys", "usr", "usr/bin"} {
		if err := v.ownedDirectory(root, name, 0o755, 0, 0); err != nil {
			return err
		}
	}
	if err := v.ownedDirectory(root, ".tools", 0o700, 0, 0); err != nil {
		return err
	}
	if err := v.bind(v.base, filepath.Join(v.root, ".tools"), true, true); err != nil {
		return err
	}
	if err := v.bind(v.boot("mnt/reader-copy/work"), filepath.Join(v.root, "work"), true, true); err != nil {
		return err
	}
	if err := v.ops.mount("", filepath.Join(v.root, "work"), "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return err
	}
	for _, mount := range []struct{ target, kind, options string }{
		{"run", "tmpfs", "mode=0755"}, {"tmp", "tmpfs", "mode=1777"}, {"var/tmp", "tmpfs", "mode=1777"},
		{"dev", "tmpfs", "mode=0755"}, {"proc", "proc", ""}, {"sys", "sysfs", ""}, {"home", "tmpfs", "mode=0755"},
	} {
		flags := uintptr(unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC)
		if mount.target == "dev" {
			flags = unix.MS_NOSUID | unix.MS_NOEXEC // Only the four native character nodes below work here.
		}
		if err := v.mount(mount.kind, filepath.Join(v.root, mount.target), mount.kind, flags, mount.options); err != nil {
			return err
		}
	}
	if err := v.ownedDirectory(root, "home/agent", 0o700, AgentUID, AgentGID); err != nil {
		return err
	}
	// No tty/ptmx, block devices, FUSE, agents disk, state or copied home.
	for _, node := range []struct {
		name  string
		minor uint32
	}{{"null", 3}, {"zero", 5}, {"random", 8}, {"urandom", 9}} {
		path := filepath.Join(v.root, "dev", node.name)
		if err := v.ops.node(path, unix.S_IFCHR|0o666, int(unix.Mkdev(1, node.minor))); err != nil {
			return fmt.Errorf("make fixed reader device: %w", err)
		}
		if err := v.ops.chmod(path, 0o666); err != nil {
			return err
		}
	}
	return nil
}
