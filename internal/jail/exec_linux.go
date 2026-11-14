package jail

import (
	"errors"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// procFiles are the files of /proc, besides the command's own /proc/self,
// that a command reads without a user namespace. They describe the host,
// not its processes.
var procFiles = []string{
	"/proc/cpuinfo", "/proc/meminfo", "/proc/stat", "/proc/loadavg", "/proc/uptime",
	"/proc/version", "/proc/filesystems", "/proc/sys/kernel/osrelease",
	"/proc/sys/kernel/cap_last_cap", "/proc/sys/kernel/pid_max", "/proc/sys/vm/overcommit_memory",
}

// runExec adds the command's own Landlock layer, which init's layer cannot
// give: with a user namespace, the jail's own /proc; without one, only its
// own /proc/self and procFiles. It then runs the command in its place, with
// only stdio and Spec.Files open.
func runExec(c *setup, env []string) error {
	// Never unlocked: the Landlock layer holds for this thread, which runs
	// execve.
	runtime.LockOSThread()
	rs, err := commandRules(c)
	if err != nil {
		return err
	}
	own := Read
	for _, r := range c.Rules {
		if r.FD == 0 {
			own |= r.Access
		}
	}
	if c.Userns {
		err = rs.addPath("/proc", Read)
	} else {
		for _, f := range procFiles {
			if err = rs.addPath(f, Read); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = rs.addPath(procSelf, own)
	}
	if err != nil {
		return err
	}
	if err := cloexecFrom(3 + c.Files); err != nil {
		return err
	}
	if err := rs.enforce(); err != nil {
		return err
	}
	if c.ExecFD > 0 {
		err = execveat(c.ExecFD, c.Argv, env)
	} else {
		err = unix.Exec(c.Argv[0], c.Argv, env)
	}
	return failure("cannot run "+quote(c.Argv[0])+" in the jail: "+err.Error(),
		"Check that a Bind shows the program, and the libraries that it loads.")
}

// execveat runs the file that fd holds, as fexecve does. Before Linux 3.19,
// which has no execveat, it runs /proc/self/fd/<fd>.
func execveat(fd int, argv, env []string) error {
	argvp, err := syscall.SlicePtrFromStrings(argv)
	if err != nil {
		return err //nolint:wrapcheck // the caller adds the program
	}
	envp, err := syscall.SlicePtrFromStrings(env)
	if err != nil {
		return err //nolint:wrapcheck // the caller adds the program
	}
	empty := []byte{0}
	//nolint:gosec // G103: execveat(2) takes the addresses of its strings
	_, _, errno := unix.Syscall6(unix.SYS_EXECVEAT, uintptr(fd), uintptr(unsafe.Pointer(&empty[0])),
		uintptr(unsafe.Pointer(&argvp[0])), uintptr(unsafe.Pointer(&envp[0])), unix.AT_EMPTY_PATH, 0)
	runtime.KeepAlive(argvp)
	runtime.KeepAlive(envp)
	if errors.Is(errno, unix.ENOSYS) {
		return unix.Exec("/proc/self/fd/"+strconv.Itoa(fd), argv, env) //nolint:wrapcheck // the caller adds the program
	}
	return errno
}
