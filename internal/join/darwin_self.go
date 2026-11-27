//go:build darwin

package join

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/iamlakshya/simstim/internal/brand"
)

// originalMacClient reads only this running process's kernel code hash. The
// retained descriptor must pass strict codesign verification and have that
// exact code directory before and after copy. No saved PID/hash constructs it.
func originalMacClient(ctx context.Context) ([]byte, error) {
	var hash [20]byte
	_, _, errno := syscall.Syscall6(169, uintptr(os.Getpid()), 5, uintptr(unsafe.Pointer(&hash[0])), uintptr(len(hash)), 0, 0)
	runtime.KeepAlive(&hash)
	if errno != 0 {
		return nil, fmt.Errorf("read this client's kernel code identity: %w", errno)
	}
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > 256<<20 {
		return nil, macFailure("join_mac_helper", "the original client executable is unavailable or too large")
	}
	verify := func() error {
		for _, args := range [][]string{{"--verify", "--strict", "--all-architectures", "/dev/fd/3"}, {"--display", "--verbose=4", "/dev/fd/3"}} {
			command := exec.CommandContext(ctx, "/usr/bin/codesign", args...)
			command.ExtraFiles = []*os.File{file}
			command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
			var output macOutput
			command.Stdout, command.Stderr = &output, &output
			if err := command.Run(); err != nil || output.overflow {
				return errors.Join(err, macFailure("join_mac_helper", "the retained client descriptor failed native signature verification"))
			}
			if args[0] == "--display" && !containsMacLine(string(output.data), "CDHash="+hex.EncodeToString(hash[:])) {
				return macFailure("join_mac_helper", "the retained client file differs from this actual running program")
			}
		}
		return nil
	}
	if err := verify(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 256<<20+1))
	if err != nil || len(data) > 256<<20 {
		return nil, errors.Join(err, macFailure("join_mac_helper", "the original client copy exceeds its bound"))
	}
	if err := verify(); err != nil {
		clear(data)
		return nil, err
	}
	after, err := file.Stat()
	if err != nil {
		clear(data)
		return nil, err
	}
	first, last := before.Sys().(*syscall.Stat_t), after.Sys().(*syscall.Stat_t)
	if !os.SameFile(before, after) || before.Size() != after.Size() || first.Ctimespec != last.Ctimespec || !before.ModTime().Equal(after.ModTime()) {
		clear(data)
		return nil, macFailure("join_mac_helper", "the original client changed during its native verified copy")
	}
	return data, nil
}

func (e *macExecutor) installOriginalHelper(ctx context.Context) error {
	parent := "/usr/local/libexec"
	for _, path := range []string{"/usr/local", parent, filepath.Join(parent, brand.Name), filepath.Join(parent, brand.Name, "mac"), filepath.Dir(macHelperPath(e.ledger.Account))} {
		if err := macRootDir(path, 0755); err != nil {
			return err
		}
	}
	data, err := originalMacClient(ctx)
	if err != nil {
		return err
	}
	defer clear(data)
	if err := e.installFile("helper", macHelperPath(e.ledger.Account), data, 0555); err != nil {
		return err
	}
	leave := []byte("#!/bin/sh\nset -eu\n[ \"$(/usr/bin/id -u)\" = 0 ] || exit 126\nexec '" + macHelperPath(e.ledger.Account) + "' host _join-leave --account '" + e.ledger.Account + "'\n")
	if err := e.installFile("leave", filepath.Join(e.dir, "leave.sh"), leave, 0700); err != nil {
		return err
	}
	return e.installFile("facts-dispatcher", macFactsDispatcher(e.ledger.Account), macDispatcherScript(e.ledger.Account), 0555)
}
