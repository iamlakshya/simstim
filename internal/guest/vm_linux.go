//go:build linux

package guest

import (
	"context"
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
	cfg := parseMicroVMFlags(args)
	cfg.SelfTest = selfTest
	return true, runMicroVMInit(cfg)
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

type vmMountState struct {
	sessionMount string
	stateMounts  []string
	hasUserRoot  bool
}

func runMicroVMInit(cfg vmBootConfig) error {
	if err := mountBasePseudoFS(cfg); err != nil {
		vmLog("mount pseudo fs failed: %v", err)
		shutdownMicroVM(microVMShutdownConfig{})
		return err
	}

	if cfg.SelfTest {
		return runSelfTest()
	}

	mstate, err := setupMicroVMRoot(cfg)
	if err != nil {
		vmLog("setup root failed: %v", err)
		shutdownMicroVM(microVMShutdownConfig{SessionMount: mstate.sessionMount, StateMounts: mstate.stateMounts})
		return err
	}

	var dockerGID int
	if mstate.hasUserRoot {
		dockerGID, err = configureGuestEtc("/", cfg)
		if err != nil {
			vmLog("configure /etc failed: %v", err)
			shutdownMicroVM(microVMShutdownConfig{SessionMount: mstate.sessionMount, StateMounts: mstate.stateMounts})
			return err
		}
	}
	_ = unix.Sethostname([]byte("sandbox"))

	if err := setupMicroVMNetwork(DefaultTransparentPort, cfg.Docker); err != nil {
		vmLog("setup network failed: %v", err)
		if mstate.hasUserRoot {
			shutdownMicroVM(microVMShutdownConfig{SessionMount: mstate.sessionMount, StateMounts: mstate.stateMounts})
			return err
		}
		_ = bringUpLoopback()
	}

	agentCgroupDir, cgErr := configureGuestCgroups(defaultGuestCgroupRoot, "/proc/meminfo", os.Getpid())
	if cgErr != nil {
		vmLog("configure guest cgroups failed: %v", cgErr)
	}

	runDir := filepath.Join("/run", brand.Name)
	_ = chownMicroVMDir(runDir, 0, 0, 0o755)
	_ = chownMicroVMDir(filepath.Join(runDir, "hooks"), GuestHolderUID, GuestAgentGID, 0o750)
	_ = chownMicroVMDir(filepath.Join(runDir, "paste"), GuestAgentUID, GuestAgentGID, 0o700)

	var dockerMgr *dockerManager
	if cfg.Docker && findDockerd() != "" {
		dm, derr := startDockerActivator(context.Background(), dockerOptions{
			DockerGID: dockerGID,
			ProxyURL:  "http://127.0.0.1:3128",
		})
		if derr != nil {
			vmLog("start docker activator failed: %v", derr)
		} else {
			dockerMgr = dm
		}
	}

	ln, err := listenVsock(vmVsockPort)
	if err != nil {
		vmLog("listen vsock %d failed: %v", vmVsockPort, err)
		shutdownMicroVM(microVMShutdownConfig{Docker: dockerMgr, SessionMount: mstate.sessionMount, StateMounts: mstate.stateMounts})
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
		shutdownMicroVM(microVMShutdownConfig{Docker: dockerMgr, SessionMount: mstate.sessionMount, StateMounts: mstate.stateMounts})
		return err
	}

	// Forward power button press while RunConnWithOptions is active via SIGTERM.
	doneRun := make(chan struct{})
	go func() {
		select {
		case <-powerCh:
			_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		case <-doneRun:
		}
	}()

	err = RunConnWithOptions(conn, runDir, RunOptions{
		Proxy:           true,
		DNS:             true,
		Transparent:     true,
		TransparentAddr: transparentListenAddr(cfg.Docker),
		MicroVM:         true,
		ReportMem:       true,
		AgentCgroupDir:  agentCgroupDir,
		Sudo:            cfg.Sudo,
		Docker:          cfg.Docker,
		DockerGID:       dockerGID,
	})
	close(doneRun)
	shutdownMicroVM(microVMShutdownConfig{
		Docker:       dockerMgr,
		SessionMount: mstate.sessionMount,
		StateMounts:  mstate.stateMounts,
	})
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
	shutdownMicroVM(microVMShutdownConfig{})
	return nil
}

func mountBasePseudoFS(cfg vmBootConfig) error {
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
	_ = mkdirMount("/dev/shm")
	_ = unix.Mount("tmpfs", "/dev/shm", "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=1777")
	_ = mkdirMount("/sys/fs/cgroup")
	if err := unix.Mount("cgroup2", "/sys/fs/cgroup", "cgroup2", unix.MS_NOSUID|unix.MS_NOEXEC|unix.MS_NODEV, "nsdelegate,memory_recursiveprot"); err != nil && !errors.Is(err, unix.EBUSY) {
		_ = unix.Mount("cgroup2", "/sys/fs/cgroup", "cgroup2", unix.MS_NOSUID|unix.MS_NOEXEC|unix.MS_NODEV, "")
	}
	// Only expose /dev/fuse when sandbox.sudo or --docker is enabled (question 57).
	if !cfg.Sudo && !cfg.Docker {
		_ = os.Remove("/dev/fuse")
	}
	return nil
}

func bringUpLoopback() error { return bringUpInterface("lo") }

func vmLog(format string, args ...any) {
	msg := fmt.Sprintf("wall-init: "+format+"\n", args...)
	if f, err := os.OpenFile("/dev/console", os.O_WRONLY, 0); err == nil {
		_, _ = io.WriteString(f, msg)
		_ = f.Close()
		return
	}
	_, _ = io.WriteString(os.Stderr, msg)
}
