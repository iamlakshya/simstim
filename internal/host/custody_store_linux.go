//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type custodyStoreEntryV1 struct {
	Name         string
	Mode         uint32
	DeviceMajor  uint32
	DeviceMinor  uint32
	Inode        uint64
	RDeviceMajor uint32
	RDeviceMinor uint32
	Path         string
	Flags        uint32
}

func (main *ManagerCustodyMainV1) rolesV1() []struct {
	name string
	file *os.File
} {
	return []struct {
		name string
		file *os.File
	}{
		{"custody-root-v1", main.root}, {"custody-holder-v1", main.holder},
		{"custody-cgroup-v1", main.cgroup}, {"custody-pidfd-v1", main.pidfd},
		{"custody-claim-v1", main.claim.file},
	}
}

func (main *ManagerCustodyMainV1) storeEntriesV1(ctx context.Context) ([]custodyStoreEntryV1, error) {
	if err := main.checkLiveV1(ctx); err != nil {
		return nil, err
	}
	entries, err := main.manager.dumpCustodyStoreV1(ctx, UnitName(main.owner.native.id))
	return entries, errors.Join(err, main.checkLiveV1(ctx))
}

func (manager *nativeUserManagerV1) dumpCustodyStoreV1(ctx context.Context, unit string) ([]custodyStoreEntryV1, error) {
	if err := errors.Join(ctx.Err(), manager.checkNativeManager()); err != nil {
		return nil, err
	}
	var entries []custodyStoreEntryV1
	err := manager.bus.Object("org.freedesktop.systemd1", systemdManagerPath).CallWithContext(ctx, systemdManagerInterface+".DumpUnitFileDescriptorStore", 0, unit).Store(&entries)
	return entries, errors.Join(err, manager.checkNativeManager(), ctx.Err())
}

func (main *ManagerCustodyMainV1) verifyStoreV1(ctx context.Context) error {
	entries, err := main.storeEntriesV1(ctx)
	if err != nil {
		return err
	}
	roles := main.rolesV1()
	if len(entries) != len(roles) {
		return sessionLeaseError("the manager did not retain the exact native custody bundle")
	}
	seen := make(map[string]bool, len(roles))
	for _, entry := range entries {
		var file *os.File
		for _, role := range roles {
			if role.name == entry.Name {
				file = role.file
			}
		}
		if file == nil || seen[entry.Name] {
			return sessionLeaseError("the native manager custody store has extra or ambiguous roles")
		}
		seen[entry.Name] = true
		var stat unix.Stat_t
		if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
			return err
		}
		flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
		if err != nil || entry.Mode != stat.Mode || entry.DeviceMajor != unix.Major(uint64(stat.Dev)) || entry.DeviceMinor != unix.Minor(uint64(stat.Dev)) || entry.Inode != stat.Ino || entry.RDeviceMajor != unix.Major(uint64(stat.Rdev)) || entry.RDeviceMinor != unix.Minor(uint64(stat.Rdev)) || entry.Flags != custodyManagerFlagsV1(uint32(flags)) {
			return errors.Join(sessionLeaseError("the manager retained another native object for a custody role"), err)
		}
	}
	return nil
}

// DepositAfterQuiescenceV1 sends only this original main's concrete handles.
// It refuses every non-self process or child cgroup before deposit and keeps
// all local copies on failure. A processed barrier alone never proves custody.
func (main *ManagerCustodyMainV1) DepositAfterQuiescenceV1(ctx context.Context) error {
	if main == nil {
		return sessionLeaseError("the original custody producer is absent")
	}
	main.mu.Lock()
	defer main.mu.Unlock()
	if err := main.checkLiveV1(ctx); err != nil {
		return err
	}
	if main.retainedStop {
		return nil
	}
	if main.deposited {
		return main.verifyStoreV1(ctx)
	}
	// The same per-user admission lock covers measured headroom, transfer,
	// barrier and exact acceptance. Simultaneous stops cannot spend the same
	// manager FD headroom, and no reload can race this custody transition.
	config, err := main.manager.lockCustodyConfigV1(ctx)
	if err != nil {
		return err
	}
	defer config.Close()
	if err := main.manager.checkCustodyPinV1(ctx, UnitName(main.owner.native.id)); err != nil {
		return err
	}
	if err := custodySelfOnlyV1(main.cgroup); err != nil {
		return err
	}
	entries, err := main.storeEntriesV1(ctx)
	if err != nil || len(entries) != 0 {
		return errors.Join(sessionLeaseError("native custody requires the original empty manager store"), err)
	}
	if err := main.manager.custodyFDHeadroomV1(); err != nil {
		return err
	}
	for _, role := range main.rolesV1() {
		if err := main.notifyCustodyV1(ctx, "FDSTORE=1\nFDPOLL=0\nFDNAME="+role.name, role.file); err != nil {
			return err
		}
	}
	if err := main.notifyBarrierV1(ctx); err != nil {
		return err
	}
	if err := main.verifyStoreV1(ctx); err != nil {
		return err
	}
	main.deposited = true
	return nil
}

func (main *ManagerCustodyMainV1) notifyCustodyV1(ctx context.Context, payload string, file *os.File) error {
	if err := main.checkLiveV1(ctx); err != nil {
		return err
	}
	return errors.Join(main.manager.notifyNativeV1(ctx, payload, file), main.checkLiveV1(ctx))
}
func (main *ManagerCustodyMainV1) notifyBarrierV1(ctx context.Context) error {
	if err := main.checkLiveV1(ctx); err != nil {
		return err
	}
	return errors.Join(main.manager.notifyNativeBarrierV1(ctx), main.checkLiveV1(ctx))
}

func (manager *nativeUserManagerV1) notifyNativeV1(ctx context.Context, payload string, file *os.File) error {
	if err := errors.Join(ctx.Err(), manager.checkNativeManager()); err != nil {
		return err
	}
	if os.Getenv("NOTIFY_SOCKET") != manager.rootPath+"/systemd/notify" {
		return sessionLeaseError("the original main did not inherit its actual native manager notification endpoint")
	}
	// The ambient NOTIFY_SOCKET is not an authority. Send through the same
	// held private runtime directory as the authenticated native manager.
	before, err := manager.root.Lstat("systemd/notify")
	if err != nil {
		return err
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || before.Mode()&os.ModeSocket == 0 || stat.Uid != uint32(os.Getuid()) {
		return sessionLeaseError("the original manager notification endpoint has another native owner or type")
	}
	directory, err := manager.root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	descriptor, err := unix.Socket(unix.AF_UNIX, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer unix.Close(descriptor)
	address := &unix.SockaddrUnix{Name: fmt.Sprintf("/proc/self/fd/%d/systemd/notify", directory.Fd())}
	var rights []byte
	if file != nil {
		rights = unix.UnixRights(int(file.Fd()))
	}
	n, err := unix.SendmsgN(descriptor, []byte(payload), rights, address, unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
	if err != nil || n != len(payload) {
		return errors.Join(sessionLeaseError("the original main could not deliver its complete custody notification"), err)
	}
	after, err := manager.root.Lstat("systemd/notify")
	if err != nil || !os.SameFile(before, after) {
		return errors.Join(sessionLeaseError("the original notification endpoint changed during native deposit"), err)
	}
	return errors.Join(ctx.Err(), manager.checkNativeManager())
}

func (manager *nativeUserManagerV1) notifyNativeBarrierV1(ctx context.Context) error {
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	if err := manager.notifyNativeV1(ctx, "BARRIER=1", writer); err != nil {
		return errors.Join(err, writer.Close())
	}
	if err := writer.Close(); err != nil {
		return err
	}
	deadline, bounded := ctx.Deadline()
	if !bounded {
		return sessionLeaseError("the custody notification barrier has no original bounded budget")
	}
	if err := reader.SetReadDeadline(deadline); err != nil {
		return err
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(interrupted); _ = reader.SetReadDeadline(time.Now()) })
	defer func() {
		if !stop() {
			<-interrupted
		}
	}()
	var byteValue [1]byte
	n, err := reader.Read(byteValue[:])
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if n != 0 || err != io.EOF {
		return errors.Join(sessionLeaseError("the manager did not complete the separate custody barrier"), err)
	}
	return errors.Join(ctx.Err(), manager.checkNativeManager())
}

func (manager *nativeUserManagerV1) custodyFDHeadroomV1() error {
	if err := manager.checkNativeManager(); err != nil {
		return err
	}
	limits, err := manager.peer.proc.ReadFile("limits")
	if err != nil {
		return err
	}
	var soft uint64
	for _, line := range strings.Split(string(limits), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 6 && strings.Join(fields[:3], " ") == "Max open files" {
			if fields[3] == "unlimited" {
				soft = ^uint64(0)
			} else {
				soft, err = strconv.ParseUint(fields[3], 10, 64)
			}
		}
	}
	if err != nil || soft == 0 {
		return errors.Join(sessionLeaseError("the native manager FD headroom could not be measured"), err)
	}
	directory, err := manager.peer.proc.Open("fd")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closed := directory.Close()
	// Five retained roles and the separate temporary barrier descriptor. This
	// derives from actual kernel limits, not an arbitrary retained-session cap.
	if readErr != nil || closed != nil || soft < uint64(len(entries))+custodyBundleSizeV1+1 {
		return errors.Join(sessionLeaseError("the original manager has insufficient measured FD headroom for custody"), readErr, closed)
	}
	return manager.checkNativeManager()
}
