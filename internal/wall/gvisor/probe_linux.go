//go:build linux

package gvisor

import (
	"bytes"
	"syscall"
)

func kernelRelease() string {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return ""
	}
	raw := make([]byte, 0, len(u.Release))
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		raw = append(raw, byte(c)) //nolint:gosec // G115: UTS release bytes are ASCII
	}
	return string(bytes.TrimSpace(raw))
}
