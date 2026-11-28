//go:build linux

package guest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/iamlakshya/simstim/internal/brand"
)

const vmVsockPort = 1024

// maybeRunMicroVM handles microvm PID 1 boot (--self-test or full overlay root
// + AF_VSOCK listener) when wall-init starts as /init (PID 1) inside a microvm
// without an inherited link socketpair on linkFD (fd 3). Container or OCI walls
// where wall-init runs as PID 1 (such as gVisor runsc or bwrap --as-pid-1)
// pass a socketpair on fd 3 and invoke the host helper binary, so they stay on
// the standard linkFD path.
// Returns (true, err) when it handled execution as a microvm init.
func maybeRunMicroVM(args []string) (bool, error) {
	arg0 := ""
	if len(os.Args) > 0 {
		arg0 = os.Args[0]
	}
	handled, selfTest := shouldRunMicroVM(os.Getpid(), arg0, args, isSocketFD(linkFD))
	if !handled {
		return false, nil
	}
	return true, runMicroVMInit(selfTest)
}

func shouldRunMicroVM(pid int, arg0 string, args []string, linkIsSocket bool) (handled, selfTest bool) {
	for _, a := range args {
		if a == "--self-test" {
			return true, true
		}
	}
	if pid != 1 || linkIsSocket {
		return false, false
	}
	if filepath.Base(arg0) != "init" {
		return false, false
	}
	return true, false
}

func isSocketFD(fd int) bool {
	_, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	return err == nil
}

func runMicroVMInit(selfTest bool) error {
	if err := mountBasePseudoFS(); err != nil {
		vmLog("mount pseudo fs failed: %v", err)
		powerOffIfPID1()
		return err
	}

	if selfTest {
		return runSelfTest()
	}

	if err := setupMicroVMRoot(); err != nil {
		vmLog("setup root failed: %v", err)
		powerOffIfPID1()
		return err
	}
	_ = bringUpLoopback()
	_ = unix.Sethostname([]byte("sandbox"))

	ln, err := listenVsock(vmVsockPort)
	if err != nil {
		vmLog("listen vsock %d failed: %v", vmVsockPort, err)
		powerOffIfPID1()
		return err
	}
	defer ln.Close()

	// Watch ACPI power button (Generic Netlink acpi_event + /dev/input/event*)
	// and signals so ch-remote power-button cleanly shuts down the guest.
	powerCh := make(chan struct{})
	var powerOnce sync.Once
	triggerPower := func() {
		powerOnce.Do(func() { close(powerCh) })
	}
	stopACPI := watchACPIPowerButton(triggerPower)
	defer stopACPI()
	stopInput := watchInputPowerButton(triggerPower)
	defer stopInput()

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGPWR)
	defer signal.Stop(sigCh)

	accepted := make(chan struct{})
	go func() {
		select {
		case <-powerCh:
			_ = ln.Close()
		case <-sigCh:
			_ = ln.Close()
		case <-accepted:
		}
	}()

	conn, err := ln.Accept()
	close(accepted)
	if err != nil {
		vmLog("accept vsock failed: %v", err)
		powerOffIfPID1()
		return err
	}

	// Forward power button press while RunConn is active via SIGTERM to PID 1.
	doneRun := make(chan struct{})
	go func() {
		select {
		case <-powerCh:
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		case <-doneRun:
		}
	}()

	runDir := filepath.Join("/run", brand.Name)
	err = RunConn(conn, runDir)
	close(doneRun)
	powerOffIfPID1()
	return err
}

func runSelfTest() error {
	pid := os.Getpid()
	mounts, _ := os.ReadFile("/proc/mounts")
	hasDM0 := strings.Contains(string(mounts), "/dev/dm-0") || strings.Contains(string(mounts), " erofs ")
	if pid == 1 && hasDM0 {
		vmLog("self-test ok")
	} else {
		vmLog("self-test failed: pid=%d dm0=%v", pid, hasDM0)
	}
	powerOffIfPID1()
	return nil
}

func mountBasePseudoFS() error {
	mounts := []struct {
		src, target, fstype string
		flags               uintptr
		data                string
	}{
		{"proc", "/proc", "proc", unix.MS_NOSUID | unix.MS_NOEXEC | unix.MS_NODEV, ""},
		{"sysfs", "/sys", "sysfs", unix.MS_NOSUID | unix.MS_NOEXEC | unix.MS_NODEV, ""},
		{"devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"},
		{"tmpfs", "/run", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=0755"},
		{"tmpfs", "/tmp", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
	}
	for _, m := range mounts {
		if err := unix.Mount(m.src, m.target, m.fstype, m.flags, m.data); err != nil && !errors.Is(err, unix.EBUSY) {
			return fmt.Errorf("mount %s on %s: %w", m.fstype, m.target, err)
		}
	}
	_ = mkdirMount("/dev/pts")
	if err := unix.Mount("devpts", "/dev/pts", "devpts", unix.MS_NOSUID|unix.MS_NOEXEC, "gid=5,mode=0620,ptmxmode=0666"); err != nil && !errors.Is(err, unix.EBUSY) {
		return fmt.Errorf("mount devpts: %w", err)
	}
	return nil
}

func mkdirMount(dir string) error {
	//nolint:gosec // G301: guest root mountpoint dirs require 0755 traversal by non-root agent uid
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return nil
}

func setupMicroVMRoot() error {
	// Ensure private mount propagation so MS_MOVE and pivot_root never fail with EINVAL.
	_ = unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, "")
	if _, statErr := os.Stat("/dev/dm-1"); statErr != nil {
		if os.IsNotExist(statErr) {
			// No user rootfs disk (for example a minimal tools-only boot).
			return nil
		}
		return fmt.Errorf("stat /dev/dm-1: %w", statErr)
	}
	if err := unix.Mount("/dev/dm-1", "/mnt/image", "ext4", unix.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("mount verified rootfs /dev/dm-1: %w", err)
	}
	for _, d := range []string{"/run/overlay/upper", "/run/overlay/work"} {
		if err := mkdirMount(d); err != nil {
			return err
		}
	}
	ovOpts := "lowerdir=/mnt/image,upperdir=/run/overlay/upper,workdir=/run/overlay/work"
	if err := unix.Mount("overlay", "/mnt/root", "overlay", 0, ovOpts); err != nil {
		return fmt.Errorf("mount overlay root: %w", err)
	}

	// Expose the read-only dm-verity tools filesystem inside /run so holders
	// can exec the helper binary without copying it into RAM.
	toolsMount := filepath.Join("/run", brand.Name, "tools")
	if err := mkdirMount(toolsMount); err != nil {
		return err
	}
	if err := unix.Mount("/", toolsMount, "", unix.MS_BIND|unix.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("bind tools root: %w", err)
	}
	_ = unix.Mount("", toolsMount, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY, "")

	serials := blockDisksBySerial()
	if dev, ok := serials["agents"]; ok {
		if err := mountAgentsDisk("/dev/"+dev, "/mnt/agents", "/mnt/root/opt/agents"); err != nil {
			return err
		}
	}
	if dev, ok := serials["session"]; ok {
		if err := mountSessionDisk("/dev/"+dev, "/mnt/session", "/mnt/root/work"); err != nil {
			return err
		}
	}

	// Move mounted pseudo-filesystems into the new overlay root before pivot_root.
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

	_ = os.Setenv(brand.Env("GUEST_HELPER"), filepath.Join("/run", brand.Name, "tools", "init"))
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

func mountSessionDisk(devPath, stageDir, workTarget string) error {
	if err := unix.Mount(devPath, stageDir, "ext4", unix.MS_NODEV|unix.MS_NOSUID, ""); err != nil {
		return fmt.Errorf("mount session disk %s: %w", devPath, err)
	}
	workSrc := filepath.Join(stageDir, "work")
	if err := mkdirMount(workSrc); err != nil {
		return err
	}
	_ = os.Chown(workSrc, 1000, 1000)
	if err := mkdirMount(workTarget); err != nil {
		return err
	}
	if err := unix.Mount(workSrc, workTarget, "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind session /work: %w", err)
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

func bringUpLoopback() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open loopback socket: %w", err)
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return fmt.Errorf("prepare lo ifreq: %w", err)
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("get lo flags: %w", err)
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("set lo up: %w", err)
	}
	return nil
}

func vmLog(format string, args ...any) {
	msg := fmt.Sprintf("wall-init: "+format+"\n", args...)
	if f, err := os.OpenFile("/dev/console", os.O_WRONLY, 0); err == nil {
		_, _ = io.WriteString(f, msg)
		_ = f.Close()
		return
	}
	_, _ = io.WriteString(os.Stderr, msg)
}

func powerOffIfPID1() {
	if os.Getpid() != 1 {
		return
	}
	unix.Sync()
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}
