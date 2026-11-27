//go:build linux

package host

import (
	"context"
	"debug/elf"
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// This checks only whether the actual fixed start may rely on execute-only
// kernel entry protection. It is not code ownership or an execution grant.
// Root/capable and interpreted images need a different genuine producer.
func checkPurgeKernelExecEligibilityV2(ctx context.Context, file *os.File) error {
	if ctx == nil || file == nil {
		return placementError("the fixed purge image has no actual eligibility observation")
	}
	if err := errors.Join(ctx.Err(), checkPurgeExecCredentialsV2()); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.Join(placementError("the actual purge image is not a regular native ELF"), err)
	}
	_, _, uid, links, known := footprintIdentity(info)
	if !known || uid != uint64(os.Getuid()) || links != 1 {
		return placementError("the actual purge image has another owner or alias")
	}
	image, err := elf.NewFile(file)
	if err != nil {
		return errors.Join(placementError("the actual purge image is not a verified native ELF"), err)
	}
	defer image.Close()
	if image.Class != elf.ELFCLASS64 || image.Data != elf.ELFDATA2LSB || image.Type != elf.ET_EXEC && image.Type != elf.ET_DYN {
		return placementError("the actual purge ELF has an unsupported native execution class")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" || runtime.GOARCH == "amd64" && image.Machine != elf.EM_X86_64 || runtime.GOARCH == "arm64" && image.Machine != elf.EM_AARCH64 {
		return placementError("the actual purge ELF is not native to this supported process architecture")
	}
	for _, program := range image.Progs {
		if program.Type == elf.PT_INTERP {
			return placementError("the copied purge image requires an interpreter and has no direct kernel entry guarantee")
		}
	}
	_, err = unix.Fgetxattr(int(file.Fd()), "security.capability", nil)
	if !errors.Is(err, unix.ENODATA) {
		return errors.Join(placementError("the actual purge image has file capabilities or unknown capability metadata"), err)
	}
	return ctx.Err()
}

func checkPurgeExecCredentialsV2() (returned error) {
	if os.Getuid() == 0 || os.Geteuid() != os.Getuid() {
		return placementError("the temporary purge kernel-entry class does not support root or changed effective credentials")
	}
	file, err := os.Open("/proc/self/status")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, file.Close()) }()
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &filesystem); err != nil || filesystem.Type != unix.PROC_SUPER_MAGIC {
		return errors.Join(placementError("the actual credential observation is not kernel procfs"), err)
	}
	raw, err := io.ReadAll(io.LimitReader(file, 64<<10+1))
	if err != nil || len(raw) > 64<<10 {
		return errors.Join(placementError("the actual kernel credential observation exceeds its bound"), err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		key := fields[0]
		if key == "Uid:" {
			if seen[key] || len(fields) != 5 {
				return placementError("the actual UID observation is unknown")
			}
			seen[key] = true
			for _, value := range fields[1:] {
				uid, err := strconv.ParseUint(value, 10, 32)
				if err != nil || uid != uint64(os.Getuid()) {
					return placementError("the actual real, effective, saved or filesystem UID differs")
				}
			}
		}
		if key == "CapInh:" || key == "CapPrm:" || key == "CapEff:" || key == "CapAmb:" {
			if seen[key] || len(fields) != 2 {
				return placementError("the actual capability observation is unknown")
			}
			seen[key] = true
			value, err := strconv.ParseUint(fields[1], 16, 64)
			if err != nil || value != 0 {
				return placementError("the actual purge creator has capabilities which defeat the execute-only entry class")
			}
		}
	}
	for _, key := range []string{"Uid:", "CapInh:", "CapPrm:", "CapEff:", "CapAmb:"} {
		if !seen[key] {
			return placementError("the actual kernel credential observation is incomplete")
		}
	}
	return nil
}
