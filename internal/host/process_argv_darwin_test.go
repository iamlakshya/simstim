//go:build darwin

package host

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// ownedProcessArgs reads argv for the live process object this test started.
// The kernel buffer can contain environment data, which is never returned.
func ownedProcessArgs(process *os.Process) ([]byte, error) {
	if process == nil || process.Pid <= 0 {
		return nil, errors.New("the test has no owned agent process")
	}
	raw, err := unix.SysctlRaw("kern.procargs2", process.Pid)
	if err != nil {
		return nil, fmt.Errorf("inspect the owned agent's argv: %w", err)
	}
	argv, err := darwinProcessArgv(raw)
	if err != nil {
		return nil, err
	}
	return []byte(strings.Join(argv, " ")), nil
}

func darwinProcessArgv(raw []byte) ([]string, error) {
	// Keep every scan bounded. Neither an error nor a partial result may
	// expose the bytes after the declared argument vector.
	if len(raw) < 4 || len(raw) > 2<<20 {
		return nil, errors.New("the owned agent's argv buffer has an invalid size")
	}
	argc := int(int32(binary.NativeEndian.Uint32(raw[:4])))
	if argc < 1 || argc > 65536 || argc > len(raw)-4 {
		return nil, errors.New("the owned agent's argument count is invalid")
	}
	cursor := 4
	end := bytes.IndexByte(raw[cursor:], 0)
	if end < 0 {
		return nil, errors.New("the owned agent's executable path is incomplete")
	}
	cursor += end + 1
	for cursor < len(raw) && raw[cursor] == 0 {
		cursor++
	}
	argv := make([]string, 0, argc)
	for range argc {
		if cursor >= len(raw) {
			return nil, errors.New("the owned agent's argument vector is incomplete")
		}
		end = bytes.IndexByte(raw[cursor:], 0)
		if end < 0 {
			return nil, errors.New("an owned agent argument is incomplete")
		}
		argv = append(argv, string(raw[cursor:cursor+end]))
		cursor += end + 1
	}
	return argv, nil
}
