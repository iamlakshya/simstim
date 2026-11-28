//go:build linux

package guest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/brand"
)

func mkdirMount(dir string) error {
	//nolint:gosec // G301: guest root mountpoint dirs require 0755 traversal by non-root agent uid
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return nil
}

// microVMSysDir returns the root-only (0:0 0700) parent directory under /run
// that holds the raw session disk mount and the overlay upper/work dirs so
// uid 1000 (agent) cannot traverse or tamper with them.
func microVMSysDir() string { return filepath.Join("/run", brand.Name, "sys") }

func setupMicroVMRoot(cfg vmBootConfig) (vmMountState, error) {
	var state vmMountState
	// Ensure private mount propagation so MS_MOVE and pivot_root never fail with EINVAL.
	_ = unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, "")
	if _, statErr := os.Stat("/dev/dm-1"); statErr != nil {
		if os.IsNotExist(statErr) {
			return state, nil
		}
		return state, fmt.Errorf("stat /dev/dm-1: %w", statErr)
	}
	if err := unix.Mount("/dev/dm-1", "/mnt/image", "ext4", unix.MS_RDONLY, ""); err != nil {
		return state, fmt.Errorf("mount verified rootfs /dev/dm-1: %w", err)
	}

	serials := blockDisksBySerial()
	sessMount, err := mountMicroVMSessionDisk(serials["session"])
	if err != nil {
		return state, err
	}
	state.sessionMount = sessMount

	if err := mountOverlayAndTools(cfg.KeepSystem, sessMount); err != nil {
		return state, err
	}
	if dev, ok := serials["agents"]; ok {
		if err := mountAgentsDisk("/dev/"+dev, "/mnt/agents", "/mnt/root/opt/agents"); err != nil {
			return state, err
		}
	}
	stateMounts, err := mountGuestHomeAndWork(cfg, sessMount)
	if err != nil {
		return state, err
	}
	state.stateMounts = stateMounts

	if err := finishPivotRoot(); err != nil {
		return state, err
	}
	_ = os.Setenv(brand.Env("GUEST_HELPER"), filepath.Join("/run", brand.Name, "tools", "init"))
	state.hasUserRoot = true
	return state, nil
}

func mountMicroVMSessionDisk(devName string) (string, error) {
	if devName == "" {
		return "", nil
	}
	sysDir := microVMSysDir()
	if err := chownMicroVMDir(sysDir, 0, 0, 0o700); err != nil {
		return "", fmt.Errorf("create microvm sys dir: %w", err)
	}
	sessMount := filepath.Join(sysDir, "session")
	if err := chownMicroVMDir(sessMount, 0, 0, 0o700); err != nil {
		return "", fmt.Errorf("create session mount dir: %w", err)
	}
	if err := unix.Mount("/dev/"+devName, sessMount, "ext4", unix.MS_NODEV|unix.MS_NOSUID, ""); err != nil {
		return "", fmt.Errorf("mount session disk /dev/%s: %w", devName, err)
	}
	// EnsureSessionDisk formats the ext4 root with root_owner=1000:1000, so
	// lock down the mounted filesystem root to 0:0 0700 after mount succeeds;
	// /work and StateDirs are chowned 1000:1000 individually before binding.
	if err := os.Chown(sessMount, 0, 0); err != nil {
		return "", fmt.Errorf("chown mounted session disk: %w", err)
	}
	//nolint:gosec // G302: sessMount is a root-only directory requiring 0700 traversal by PID 1
	if err := os.Chmod(sessMount, 0o700); err != nil {
		return "", fmt.Errorf("chmod mounted session disk: %w", err)
	}
	return sessMount, nil
}

func mountOverlayAndTools(keepSystem bool, sessMount string) error {
	sysDir := microVMSysDir()
	if err := chownMicroVMDir(sysDir, 0, 0, 0o700); err != nil {
		return fmt.Errorf("create microvm sys dir: %w", err)
	}
	overlayBase := filepath.Join(sysDir, "overlay")
	if keepSystem {
		if sessMount == "" {
			return errors.New("--keep-system requires a writable session disk. Try: attach a session disk")
		}
		overlayBase = filepath.Join(sessMount, "overlay")
	}
	if err := chownMicroVMDir(overlayBase, 0, 0, 0o700); err != nil {
		return fmt.Errorf("create overlay base dir: %w", err)
	}
	upperDir := filepath.Join(overlayBase, "upper")
	workDir := filepath.Join(overlayBase, "work")
	// upperDir becomes "/" in the merged overlay (so 0755 keeps "/" traversable)
	// while overlayBase, sysDir and sessMount are all 0:0 0700 to block uid 1000.
	if err := chownMicroVMDir(upperDir, 0, 0, 0o755); err != nil {
		return fmt.Errorf("create overlay upper dir: %w", err)
	}
	if err := chownMicroVMDir(workDir, 0, 0, 0o700); err != nil {
		return fmt.Errorf("create overlay work dir: %w", err)
	}
	ovOpts := fmt.Sprintf("lowerdir=/mnt/image,upperdir=%s,workdir=%s", upperDir, workDir)
	if err := unix.Mount("overlay", "/mnt/root", "overlay", 0, ovOpts); err != nil {
		return fmt.Errorf("mount overlay root: %w", err)
	}
	toolsMount := filepath.Join("/run", brand.Name, "tools")
	if err := mkdirMount(toolsMount); err != nil {
		return err
	}
	if err := unix.Mount("/", toolsMount, "", unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("bind tools root: %w", err)
	}
	_ = unix.Mount("", toolsMount, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, "")
	return nil
}

func mountGuestHomeAndWork(cfg vmBootConfig, sessMount string) ([]string, error) {
	// Fresh tmpfs for /home/agent on every start (even under --keep-system)
	// so ~/.bashrc and login scripts never persist across stops (question 8).
	homeTarget := "/mnt/root/home/agent"
	if err := mkdirMount(homeTarget); err != nil {
		return nil, err
	}
	homeOpts := fmt.Sprintf("mode=0700,uid=%d,gid=%d", GuestAgentUID, GuestAgentGID)
	if err := unix.Mount("tmpfs", homeTarget, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, homeOpts); err != nil {
		return nil, fmt.Errorf("mount fresh /home/agent tmpfs: %w", err)
	}
	var stateMounts []string
	if sessMount != "" {
		var err error
		if stateMounts, err = bindSessionPaths(sessMount, "/mnt/root/work", homeTarget); err != nil {
			return nil, err
		}
	}
	if cfg.Docker {
		if err := mountDockerDataRoot(sessMount, "/mnt/root/var/lib/docker"); err != nil {
			return nil, err
		}
		stateMounts = append(stateMounts, defaultDockerDataRoot)
	}
	if err := mkdirMount("/mnt/root/var/tmp"); err == nil {
		_ = unix.Mount("tmpfs", "/mnt/root/var/tmp", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=1777")
	}
	return stateMounts, nil
}

// mountDockerDataRoot mounts a non-overlay filesystem at /var/lib/docker so
// dockerd's overlay2 storage driver never nests overlayfs on top of overlayfs.
func mountDockerDataRoot(sessMount, target string) error {
	if err := chownMicroVMDir(target, 0, 0, 0o700); err != nil {
		return fmt.Errorf("create docker data root %s: %w", target, err)
	}
	if sessMount != "" {
		src := filepath.Join(sessMount, "docker")
		if err := chownMicroVMDir(src, 0, 0, 0o700); err != nil {
			return fmt.Errorf("create session docker dir: %w", err)
		}
		if err := unix.Mount(src, target, "", unix.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind session docker data root: %w", err)
		}
		return nil
	}
	if err := unix.Mount("tmpfs", target, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0700,uid=0,gid=0"); err != nil {
		return fmt.Errorf("mount tmpfs docker data root: %w", err)
	}
	return nil
}

func finishPivotRoot() error {
	for _, dir := range []string{"dev", "proc", "sys", "run", "tmp"} {
		dst := filepath.Join("/mnt/root", dir)
		_ = mkdirMount(dst)
		if err := unix.Mount("/"+dir, dst, "", unix.MS_MOVE, ""); err != nil {
			return fmt.Errorf("move /%s into new root: %w", dir, err)
		}
	}
	oldRoot := "/mnt/root/mnt/oldroot"
	if err := mkdirMount(oldRoot); err != nil {
		return err
	}
	if err := unix.PivotRoot("/mnt/root", oldRoot); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("chdir /: %w", err)
	}
	_ = unix.Unmount("/mnt/oldroot", unix.MNT_DETACH)
	_ = os.Remove("/mnt/oldroot")
	return nil
}

func bindSessionPaths(sessMount, workTarget, homeTarget string) ([]string, error) {
	workSrc := filepath.Join(sessMount, "work")
	if err := mkdirMount(workSrc); err != nil {
		return nil, err
	}
	_ = os.Chown(workSrc, GuestAgentUID, GuestAgentGID)
	if err := mkdirMount(workTarget); err != nil {
		return nil, err
	}
	if err := unix.Mount(workSrc, workTarget, "", unix.MS_BIND, ""); err != nil {
		return nil, fmt.Errorf("bind session /work: %w", err)
	}

	stateRoot := filepath.Join(sessMount, "state")
	if err := chownMicroVMDir(stateRoot, GuestAgentUID, GuestAgentGID, 0o700); err != nil {
		return nil, fmt.Errorf("create session state dir: %w", err)
	}
	var mounted []string
	for _, rel := range allAgentStateDirs() {
		src := filepath.Join(stateRoot, filepath.FromSlash(rel))
		if err := mkdirAllChowned(stateRoot, rel, GuestAgentUID, GuestAgentGID, 0o700); err != nil {
			return mounted, fmt.Errorf("create state dir %s: %w", rel, err)
		}
		dst := filepath.Join(homeTarget, filepath.FromSlash(rel))
		if err := mkdirAllChowned(homeTarget, rel, GuestAgentUID, GuestAgentGID, 0o700); err != nil {
			return mounted, fmt.Errorf("create home state dir %s: %w", rel, err)
		}
		if err := unix.Mount(src, dst, "", unix.MS_BIND, ""); err != nil {
			return mounted, fmt.Errorf("bind state dir %s: %w", rel, err)
		}
		mounted = append(mounted, filepath.Join("/home/agent", filepath.FromSlash(rel)))
	}
	return mounted, nil
}

func mkdirAllChowned(base, rel string, uid, gid int, mode os.FileMode) error {
	cur := base
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		if err := os.Mkdir(cur, mode); err != nil && !os.IsExist(err) {
			return fmt.Errorf("mkdir %s: %w", cur, err)
		}
		_ = os.Chown(cur, uid, gid)
	}
	return nil
}

func mountAgentsDisk(devPath, stageDir, targetDir string) error {
	flags := uintptr(unix.MS_RDONLY | unix.MS_NODEV | unix.MS_NOSUID)
	if err := unix.Mount(devPath, stageDir, "erofs", flags, ""); err != nil {
		if err2 := unix.Mount(devPath, stageDir, "ext4", flags, ""); err2 != nil {
			return fmt.Errorf("mount agents disk %s: erofs=%w; ext4=%w", devPath, err, err2)
		}
	}
	if err := mkdirMount(targetDir); err != nil {
		return err
	}
	if err := unix.Mount(stageDir, targetDir, "", unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("bind agents disk to %s: %w", targetDir, err)
	}
	if err := unix.Mount("", targetDir, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NODEV|unix.MS_NOSUID, ""); err != nil {
		return fmt.Errorf("remount agents disk ro: %w", err)
	}
	return nil
}

func blockDisksBySerial() map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "vd") {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/sys/block", name, "serial"))
		if err != nil {
			continue
		}
		serial := strings.TrimSpace(strings.TrimRight(string(b), "\x00"))
		if serial != "" {
			out[serial] = name
		}
	}
	return out
}
