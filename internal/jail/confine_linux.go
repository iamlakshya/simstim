package jail

import (
	"errors"
	"runtime"
	"unsafe"

	ll "github.com/landlock-lsm/go-landlock/landlock/syscall"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// lockDown limits the calling thread, which the stage locked: no core
// dumps, no_new_privs, not dumpable, the subuid, no capabilities, the
// Landlock ruleset and the seccomp filters. A child that this thread starts
// inherits all of them.
func lockDown(c *setup, rs *ruleset, progs [][]bpf.RawInstruction) error {
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}); err != nil {
		return failure("cannot turn off core dumps: "+err.Error(), "Report this as a bug.")
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return failure("cannot set no_new_privs: "+err.Error(), "Update the host's kernel: no_new_privs needs Linux 3.5.")
	}
	// Not dumpable: the command, a child, cannot trace this stage or open
	// its memory. execve makes the command dumpable again.
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return failure("cannot clear the dumpable flag: "+err.Error(), "Report this as a bug.")
	}
	dropBounding()
	if c.Sub != nil {
		if err := becomeSub(c); err != nil {
			return err
		}
	}
	if err := dropCaps(); err != nil {
		return err
	}
	// Credential changes can reset dumpability. Keep the early clear and
	// reassert it after the final identity change, before any child starts.
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return failure("cannot clear the dumpable flag after identity changes: "+err.Error(), "Report this as a bug.")
	}
	if c.GvisorV1Version != 1 || !c.gvisorOuterNative || c.gvisorOuterGate == nil {
		if err := rs.enforce(); err != nil {
			return err
		}
	}
	for _, p := range progs {
		if err := loadFilter(p); err != nil {
			return failure("cannot load the jail's seccomp filter: "+err.Error(),
				"Check that the host's kernel has seccomp filters (CONFIG_SECCOMP_FILTER).")
		}
	}
	return nil
}

// dropBounding empties the capability bounding set, so that no execve can
// give a capability back, even to root. Without CAP_SETPCAP the set cannot
// shrink: then no_new_privs stops any gain on its own.
func dropBounding() {
	for c := range 64 {
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(c), 0, 0, 0); errors.Is(err, unix.EINVAL) {
			return // past the kernel's last capability
		}
	}
}

// dropCaps clears the ambient, inheritable, permitted and effective sets.
func dropCaps() error {
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil && !errors.Is(err, unix.EINVAL) {
		return failure("cannot clear the ambient capabilities: "+err.Error(), "Report this as a bug.")
	}
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capset(&hdr, &data[0]); err != nil {
		return failure("cannot drop the capabilities: "+err.Error(), "Report this as a bug.")
	}
	return nil
}

// becomeSub turns this thread into the subordinate user and group, inside
// IDs 1. It keeps the login user's other groups: it never calls setgroups.
func becomeSub(c *setup) error {
	if err := unix.Setresgid(1, 1, 1); err != nil {
		return failure("cannot become the jail's subordinate group: "+err.Error(),
			"Check this user's range in /etc/subgid, or run jails without a subuid.")
	}
	if err := unix.Setresuid(1, 1, 1); err != nil {
		return failure("cannot become the jail's subordinate user: "+err.Error(),
			"Check this user's range in /etc/subuid, or run jails without a subuid.")
	}
	// A change of user clears the parent-death signal. Set it again, then
	// make sure that the outer stage did not end in between.
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0); err != nil {
		return failure("cannot set the jail's parent-death signal: "+err.Error(), "Report this as a bug.")
	}
	fds := []unix.PollFd{{Fd: int32(c.SyncFD), Events: unix.POLLIN}} //nolint:gosec // G115: a file descriptor fits in int32
	if n, err := unix.Poll(fds, 0); err != nil || n != 0 {
		return failure("the jail's outer stage ended during set-up", "Start the jail again.")
	}
	_ = unix.Close(c.SyncFD)
	return nil
}

// loadFilter loads one seccomp filter on the calling thread.
func loadFilter(raw []bpf.RawInstruction) error {
	prog := make([]unix.SockFilter, len(raw))
	for i, r := range raw {
		prog[i] = unix.SockFilter{Code: r.Op, Jt: r.Jt, Jf: r.Jf, K: r.K}
	}
	fprog := unix.SockFprog{Len: uint16(len(prog)), Filter: &prog[0]} //nolint:gosec // G115: a filter has far fewer than 65536 instructions
	//nolint:gosec // G103: seccomp(2) takes the address of the filter program
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, 0, uintptr(unsafe.Pointer(&fprog)))
	runtime.KeepAlive(prog)
	if errno != 0 {
		return errno
	}
	return nil
}

// ruleset is a Landlock ruleset at the kernel's ABI, built with the
// library's system calls. fd is -1 without Landlock: then every method
// does nothing.
type ruleset struct {
	fd  int
	abi int
}

// newRuleset makes a ruleset that handles every file right that the ABI
// knows. With NetNone it also refuses every TCP (and from ABI 10, UDP) bind
// and connect. From ABI 6, the jail cannot signal, or reach the abstract
// Unix sockets of, any process outside it.
func newRuleset(abi int, n NetMode) (*ruleset, error) {
	if abi < 1 {
		return &ruleset{fd: -1}, nil
	}
	attr := ll.RulesetAttr{HandledAccessFS: fsRights(abi), HandledAccessNet: netRights(abi, n)}
	if abi >= 6 {
		attr.Scoped = ll.ScopeAbstractUnixSocket | ll.ScopeSignal
	}
	fd, err := ll.LandlockCreateRuleset(&attr, 0)
	if err != nil {
		return nil, failure("cannot make the jail's Landlock ruleset: "+err.Error(),
			"Check that Landlock is on in the host's kernel (the lsm= boot parameter).")
	}
	return &ruleset{fd: fd, abi: abi}, nil
}

// fsRights are the file rights that a Landlock ABI knows.
func fsRights(abi int) uint64 {
	switch {
	case abi <= 0:
		return 0
	case abi == 1:
		return 1<<13 - 1
	case abi == 2:
		return 1<<14 - 1 // refer
	case abi <= 4:
		return 1<<15 - 1 // truncate
	case abi <= 8:
		return 1<<16 - 1 // ioctl on devices
	default:
		return 1<<17 - 1 // connect to Unix sockets by path
	}
}

func netRights(abi int, n NetMode) uint64 {
	switch {
	case n == NetHost || abi < 4:
		return 0
	case abi < 10:
		return ll.AccessNetBindTCP | ll.AccessNetConnectTCP
	default:
		return ll.AccessNetBindTCP | ll.AccessNetConnectTCP | ll.AccessNetBindUDP | ll.AccessNetConnectSendUDP
	}
}

// fileRights are the Landlock rights that a rule on a file, not a folder,
// may name.
const fileRights = ll.AccessFSExecute | ll.AccessFSWriteFile | ll.AccessFSReadFile |
	ll.AccessFSTruncate | ll.AccessFSIoctlDev | ll.AccessFSResolveUnix

// rights turns Access into Landlock's rights.
func rights(a Access, dir bool) uint64 {
	var set uint64
	if a&Read != 0 {
		set |= ll.AccessFSReadFile | ll.AccessFSReadDir
	}
	if a&Write != 0 {
		set |= ll.AccessFSWriteFile | ll.AccessFSTruncate | ll.AccessFSRemoveDir | ll.AccessFSRemoveFile |
			ll.AccessFSMakeDir | ll.AccessFSMakeReg | ll.AccessFSMakeSock | ll.AccessFSMakeFifo | ll.AccessFSMakeSym |
			ll.AccessFSRefer // renames and links between folders of the bind: ABI 2 and later
	}
	if a&Exec != 0 {
		set |= ll.AccessFSExecute
	}
	if a&Ioctl != 0 {
		set |= ll.AccessFSIoctlDev
	}
	if a&Connect != 0 {
		set |= ll.AccessFSResolveUnix
	}
	if !dir {
		set &= fileRights
	}
	return set
}

// add allows a beneath the object that fd holds.
func (r *ruleset) add(fd int, a Access) error {
	if r.fd < 0 {
		return nil
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return failure("cannot read a path of the jail's Landlock rules: "+err.Error(), "Report this as a bug.")
	}
	set := rights(a, st.Mode&unix.S_IFMT == unix.S_IFDIR) & fsRights(r.abi)
	if set == 0 {
		return nil
	}
	if err := ll.LandlockAddPathBeneathRule(r.fd, &ll.PathBeneathAttr{ParentFd: fd, AllowedAccess: set}, 0); err != nil {
		return failure("cannot add a Landlock rule: "+err.Error(), "Report this as a bug.")
	}
	return nil
}

// addPath opens path and allows a beneath it. A missing path is skipped.
func (r *ruleset) addPath(path string, a Access) error {
	if r.fd < 0 {
		return nil
	}
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil // a /proc file that this kernel lacks needs no rule
	}
	defer func() { _ = unix.Close(fd) }()
	return r.add(fd, a)
}

// enforce restricts the calling thread to the ruleset, then closes it.
func (r *ruleset) enforce() error {
	if r.fd < 0 {
		return nil
	}
	defer func() { _ = unix.Close(r.fd) }()
	if err := ll.LandlockRestrictSelf(r.fd, 0); err != nil {
		return failure("cannot enforce the jail's Landlock rules: "+err.Error(),
			"Check that Landlock is on in the host's kernel (the lsm= boot parameter).")
	}
	return nil
}
