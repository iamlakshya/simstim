//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

func (inherited *custodyInheritanceV1) roles() map[string]*os.File {
	return map[string]*os.File{"custody-root-v1": inherited.root, "custody-holder-v1": inherited.native.file, "custody-cgroup-v1": inherited.cgroup, "custody-pidfd-v1": inherited.pidfd, "custody-claim-v1": inherited.claim.file}
}

func (inherited *custodyInheritanceV1) verifyStore(ctx context.Context) error {
	entries, err := inherited.manager.dumpCustodyStoreV1(ctx, UnitName(inherited.native.id))
	if err != nil {
		return err
	}
	return verifyCustodyStoreShapeV1(entries, inherited.roles())
}

func verifyCustodyStoreShapeV1(entries []custodyStoreEntryV1, roles map[string]*os.File) error {
	if len(entries) != custodyBundleSizeV1 || len(roles) != custodyBundleSizeV1 {
		return sessionLeaseError("the original manager custody has another native bundle size")
	}
	seen := make(map[string]bool, custodyBundleSizeV1)
	inodes := make(map[[2]uint64]bool, custodyBundleSizeV1)
	for _, entry := range entries {
		file := roles[entry.Name]
		if file == nil || seen[entry.Name] {
			return sessionLeaseError("the manager custody has an unknown or repeated kernel role")
		}
		seen[entry.Name] = true
		var stat unix.Stat_t
		if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
			return err
		}
		identity := [2]uint64{uint64(stat.Dev), stat.Ino}
		if inodes[identity] {
			return sessionLeaseError("two custody roles alias one kernel inode and could be manager-deduplicated")
		}
		inodes[identity] = true
		flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
		if err != nil || entry.Mode != stat.Mode || entry.DeviceMajor != unix.Major(uint64(stat.Dev)) || entry.DeviceMinor != unix.Minor(uint64(stat.Dev)) || entry.Inode != stat.Ino || entry.RDeviceMajor != unix.Major(uint64(stat.Rdev)) || entry.RDeviceMinor != unix.Minor(uint64(stat.Rdev)) || entry.Flags != custodyManagerFlagsV1(uint32(flags)) {
			return errors.Join(sessionLeaseError("an inherited custody role differs from the original manager's kernel object"), err)
		}
	}
	return nil
}

// systemd's native dump masks the kernel compatibility O_LARGEFILE bit. Go
// exposes O_LARGEFILE as zero on 64-bit hosts, so retain the actual kernel
// encoding for each supported host architecture. No other flag is discarded.
func custodyManagerFlagsV1(flags uint32) uint32 {
	bit := uint32(unix.O_LARGEFILE)
	switch runtime.GOARCH {
	case "amd64":
		bit = 0x8000
	case "arm64":
		bit = 0x20000
	}
	return flags &^ bit
}
