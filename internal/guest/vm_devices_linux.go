//go:build linux

package guest

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func vmEntryGuard(pid, uid int, filesystem int64) error {
	if pid != 1 || uid != 0 || filesystem != unix.EROFS_SUPER_MAGIC_V1 {
		return vmError("microvm init requires PID1/root on the verified read-only tools filesystem")
	}
	return nil
}

func verifyVMEntry() error {
	var fs unix.Statfs_t
	if err := unix.Statfs("/", &fs); err != nil {
		return err
	}
	return vmEntryGuard(os.Getpid(), os.Geteuid(), int64(fs.Type))
}

func verifyVMToolsRoot() error {
	env := defaultSelfTestEnv()
	// Bootstrap has already mounted proc/sys/dev. Reuse the canonical P31
	// root/dm0 identity verifier without another mount or a second policy.
	env.prepareFS = func() error { return nil }
	return checkRootOnDM0(env)
}

func verifyVMDisks(read func(string) (string, error)) error {
	for _, disk := range []struct{ name, serial, readonly string }{
		{"vda", "tools", "1"}, {"vdb", "image", "1"}, {"vdc", "agents", "1"}, {"vdd", "session", "0"},
	} {
		serial, err := read("/sys/block/" + disk.name + "/serial")
		if err != nil || serial != disk.serial {
			return vmError("the VM disk order or serial is invalid")
		}
		readonly, err := read("/sys/block/" + disk.name + "/ro")
		if err != nil || readonly != disk.readonly {
			return vmError("the VM disk read-only roles are invalid")
		}
	}
	return nil
}

func vmReadKernelFile(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", err
	}
	if len(data) > 4096 {
		return "", fmt.Errorf("guest kernel metadata exceeds its bound")
	}
	return strings.TrimSpace(string(data)), nil
}
