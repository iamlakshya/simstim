//go:build linux

package guest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/files"
	"golang.org/x/sys/unix"
)

func (v *vmFiles) layout(plan files.StatePlan, allowFUSE bool) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	root, err := os.OpenRoot(v.root)
	if err != nil {
		return err
	}
	defer root.Close()
	// Image UID/mode metadata is preserved by conversion. Re-establish the
	// reserved ABI here: an agent-writable root/ancestor must never let it
	// retarget the trusted helper or another member's runtime namespace.
	for _, name := range []string{".", "etc", "home", "opt", "opt/agents", "opt/" + brand.Name, "opt/" + brand.Name + "/bin", "var", "var/lib", "var/tmp", "run", "tmp", "dev", "proc", "sys", "work"} {
		if err := v.ownedDirectory(root, name, 0o755, 0, 0); err != nil {
			return err
		}
	}
	for _, name := range []string{".tools", ".persist"} {
		if err := v.ownedDirectory(root, name, 0o700, 0, 0); err != nil {
			return err
		}
	}
	if err := v.bind(v.base, filepath.Join(v.root, ".tools"), true, true); err != nil {
		return err
	}
	if err := v.bind(v.boot("mnt/session"), filepath.Join(v.root, ".persist"), false, true); err != nil {
		return err
	}
	if err := v.bind(v.boot("mnt/agents"), filepath.Join(v.root, "opt/agents"), true, true); err != nil {
		return err
	}
	if err := v.runtimeMounts(root, allowFUSE); err != nil {
		return err
	}
	if err := v.tools(root); err != nil {
		return err
	}
	return v.persistentHome(root, plan)
}

func (v *vmFiles) runtimeMounts(root *os.Root, allowFUSE bool) error {
	for _, m := range []struct {
		target, kind string
		flags        uintptr
		data         string
	}{
		{"run", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=0755"},
		{"tmp", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
		{"var/tmp", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
		{"proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"sys", "sysfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"dev", "tmpfs", unix.MS_NOSUID | unix.MS_NOEXEC, "mode=0755"},
	} {
		if err := v.mount(m.kind, filepath.Join(v.root, m.target), m.kind, m.flags, m.data); err != nil {
			return err
		}
	}
	for _, name := range []string{"pts", "shm"} {
		if err := vmDirectory(root, "dev/"+name, 0o755); err != nil {
			return err
		}
	}
	for _, node := range []struct {
		name         string
		major, minor uint32
		mode         uint32
	}{
		{"null", 1, 3, 0o666}, {"zero", 1, 5, 0o666}, {"full", 1, 7, 0o666}, {"random", 1, 8, 0o666}, {"urandom", 1, 9, 0o666}, {"tty", 5, 0, 0o666},
	} {
		path := filepath.Join(v.root, "dev", node.name)
		if err := v.ops.node(path, unix.S_IFCHR|node.mode, int(unix.Mkdev(node.major, node.minor))); err != nil {
			return fmt.Errorf("make guest device%s: %w", node.name, err)
		}
		if err := v.ops.chmod(path, node.mode); err != nil {
			return err
		}
	}
	if allowFUSE {
		path := filepath.Join(v.root, "dev/fuse")
		if err := v.ops.node(path, unix.S_IFCHR|0o666, int(unix.Mkdev(10, 229))); err != nil {
			return fmt.Errorf("make guest FUSE device: %w", err)
		}
		if err := v.ops.chmod(path, 0o666); err != nil {
			return err
		}
	}
	if err := v.mount("devpts", filepath.Join(v.root, "dev/pts"), "devpts", unix.MS_NOSUID|unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620,gid=1000"); err != nil {
		return err
	}
	if err := root.Symlink("pts/ptmx", "dev/ptmx"); err != nil {
		return err
	}
	if err := root.Symlink("/proc/self/fd", "dev/fd"); err != nil {
		return err
	}
	if err := v.mount("tmpfs", filepath.Join(v.root, "dev/shm"), "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=1777"); err != nil {
		return err
	}
	return v.ownedDirectory(root, "run/"+brand.Name, 0o755, 0, 0)
}

func (v *vmFiles) tools(root *os.Root) error {
	for _, file := range []struct{ source, target string }{
		{v.boot("init"), "opt/" + brand.Name + "/bin/" + brand.HostBinary},
		{v.boot("bin/" + brand.VTBinary), "opt/" + brand.Name + "/bin/" + brand.VTBinary},
	} {
		placeholder, err := root.OpenFile(file.target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
		if err != nil && !os.IsExist(err) {
			return err
		}
		if placeholder != nil {
			placeholder.Close()
		}
		info, err := root.Lstat(file.target)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("guest tools target is not a regular file")
		}
		if err := v.bind(file.source, filepath.Join(v.root, file.target), true, true); err != nil {
			return err
		}
	}
	return nil
}

func (v *vmFiles) persistentHome(root *os.Root, plan files.StatePlan) error {
	session, err := os.OpenRoot(v.boot("mnt/session"))
	if err != nil {
		return err
	}
	defer session.Close()
	if err := v.ownedDirectory(session, ".", 0o700, 0, 0); err != nil {
		return err
	}
	for _, name := range []string{"state", "state/dirs", "state/files", "state/files/generations", "state/files/empty"} {
		if err := v.ownedDirectory(session, name, 0o700, 0, 0); err != nil {
			return err
		}
	}
	if err := v.ownedDirectory(session, "work", 0o700, AgentUID, AgentGID); err != nil {
		return err
	}
	if err := v.bind(v.boot("mnt/session/work"), filepath.Join(v.root, "work"), false, true); err != nil {
		return err
	}
	if err := vmDirectory(root, "home/agent", 0o700); err != nil {
		return err
	}
	if err := v.mount("tmpfs", filepath.Join(v.root, "home/agent"), "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0700,uid=1000,gid=1000"); err != nil {
		return err
	}
	for _, name := range plan.Dirs {
		if err := vmDirectory(session, filepath.Join("state/dirs", filepath.Dir(name)), 0o700); err != nil {
			return err
		}
		if err := v.ownedDirectory(session, filepath.Join("state/dirs", name), 0o700, AgentUID, AgentGID); err != nil {
			return err
		}
		if err := v.agentPath(root, name); err != nil {
			return err
		}
		if err := v.bind(v.boot("mnt/session/state/dirs/"+name), filepath.Join(v.root, "home/agent", name), false, true); err != nil {
			return err
		}
	}
	return nil
}

func (v *vmFiles) agentPath(root *os.Root, name string) error {
	current := "home/agent"
	for _, part := range strings.Split(name, "/") {
		current = filepath.Join(current, part)
		if err := v.ownedDirectory(root, current, 0o700, AgentUID, AgentGID); err != nil {
			return err
		}
	}
	return nil
}
