//go:build linux

package supply

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// CloseFileOnceV1 invokes concrete os.File.Close once, with no callback or
// execution/borrower-closure flag. Caller state owns whether this is its first
// attempt and retains this same object and every diagnostic on unknown facts.
func CloseFileOnceV1(file *os.File) FileCloseFactsV1 {
	if file == nil {
		return FileCloseFactsV1{Unknown: fmt.Errorf("the actual file close has no original object")}
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	profileErr := CheckFileCloseThreadV1()
	err := file.Close()
	facts := FileCloseFactsV1{Diagnostic: err}
	if profileErr == nil && err == nil {
		facts.Retired = true
		return facts
	}
	var operation *os.PathError
	var errno syscall.Errno
	if profileErr == nil && !errors.Is(err, os.ErrClosed) && errors.As(err, &operation) && operation.Op == "close" && errors.As(operation.Err, &errno) && errno != 0 && errno != syscall.EBADF {
		facts.Retired = true
		return facts
	}
	facts.Unknown = errors.Join(err, profileErr, fmt.Errorf("the actual file close has no supported native retirement proof; Try: retain this original owner and inspect its close profile"))
	return facts
}

// CheckFileCloseThreadV1 observes native current-thread facts only. The close
// operation checks again itself while locked; this observation authorises no
// later operation and accepts no caller-supplied profile or boolean.
func CheckFileCloseThreadV1() (returned error) {
	file, err := os.Open("/proc/thread-self/status")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, file.Close()) }()
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &fs); err != nil {
		return err
	}
	if fs.Type != unix.PROC_SUPER_MAGIC {
		return fmt.Errorf("the close profile is not native procfs")
	}
	data, err := io.ReadAll(io.LimitReader(file, (16<<10)+1))
	if err != nil || len(data) > 16<<10 {
		return errors.Join(err, fmt.Errorf("the actual close profile is unavailable or oversized"))
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "Seccomp:" && fields[0] != "TracerPid:" {
			continue
		}
		if len(fields) != 2 || seen[fields[0]] || fields[1] != "0" {
			return fmt.Errorf("the actual close thread is filtered, traced or ambiguous")
		}
		seen[fields[0]] = true
	}
	if len(seen) != 2 {
		return fmt.Errorf("the actual close thread profile is incomplete")
	}
	return nil
}
