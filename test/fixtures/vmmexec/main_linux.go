//go:build linux

// A native ELF observation fixture, never a product helper. The validator
// builds this with the pinned compiler only after admitting the native scope.
// It MUST NOT set dumpability: it reports the state inherited from real exec.
package main

import (
	"encoding/json"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	runtime.LockOSThread()
	file, err := os.Open("/proc/self/exe")
	if err != nil {
		os.Exit(21)
	}
	defer file.Close()
	until := time.Now().Add(3 * time.Second)
	var stat unix.Stat_t
	for {
		if unix.Fstat(int(file.Fd()), &stat) != nil {
			os.Exit(22)
		}
		if stat.Mode&0o777 == 0o555 {
			break
		}
		if time.Now().After(until) {
			os.Exit(23)
		}
		time.Sleep(time.Millisecond)
	}
	dumpable, _, errno := unix.Syscall6(unix.SYS_PRCTL, unix.PR_GET_DUMPABLE, 0, 0, 0, 0, 0)
	if errno != 0 {
		os.Exit(24)
	}
	_ = json.NewEncoder(os.Stdout).Encode(struct{ Dumpable, Dev, Ino, Mode uint64 }{uint64(dumpable), uint64(stat.Dev), uint64(stat.Ino), uint64(stat.Mode & 0o777)})
}
