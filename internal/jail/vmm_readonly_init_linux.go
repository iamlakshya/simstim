//go:build linux

package jail

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"
)

// SAME original C prepares everything, creates only its existing control B
// through the standard native child path, then performs its sole VMM exec.
func runVMMReadonlyInitV1(c *setup, helper *int) error {
	plan := c.ReadonlyVMMInitV1
	if err := plan.checkV1(c); err != nil {
		return err
	}
	if os.Getpid() != 1 || unix.Gettid() != 1 {
		return vmmModeFailureV1("read-only final exec lost its original namespace-init thread")
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, (64<<10)+1))
	if err != nil {
		return err
	}
	if !bytes.Equal(input, append(append([]byte(nil), plan.Runtime...), '\n')) {
		return vmmModeFailureV1("the original init input differs from its already-selected protected plan")
	}
	if err := readonlyInputOffsetsV1(); err != nil {
		return err
	}
	program, kernel := os.NewFile(3, "original-readonly-program"), os.NewFile(4, "original-readonly-kernel")
	if err := supply.CheckRuntimeDescriptor(context.Background(), plan.Kernel, kernel); err != nil {
		return err
	}
	child, err := prepareVMMReadonlyControlV1(c)
	if err != nil {
		return err
	}
	rs, err := initRules(c, *helper)
	if err != nil {
		return err
	}
	progs, err := filters(c.Seccomp, c.Net, false)
	if err != nil {
		return err
	}
	if err := lockDownReadonlyInitV1(c, rs, progs); err != nil {
		return err
	}
	child, err = child.startV1()
	if err != nil {
		return err
	}
	if err := child.publishAndReleaseV1(); err != nil {
		return err
	}
	until, err := readonlyUntilV1(c)
	if err != nil {
		return err
	}
	conn, err := vmmStageConnectionV1(c.VMMExecV1)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := proto.EnableUnixCredentialsV1(conn); err != nil {
		return err
	}
	files, err := receiveReadonlyParentV1(context.Background(), conn, until, vmmReadonlyDiskLoanV1, 1)
	if err != nil {
		return err
	}
	disk := files[0]
	defer disk.Close()
	if err := checkReadonlyDiskV1(c, disk, plan.DiskMax); err != nil {
		return err
	}
	if err := child.extra[5].Close(); err != nil {
		return err
	}
	child.extra[5] = nil
	fd, err := unix.FcntlInt(disk.Fd(), unix.F_DUPFD_CLOEXEC, 8)
	if err != nil {
		return err
	}
	if fd != 8 {
		unix.Close(fd)
		return vmmModeFailureV1("the consumed final disk slot is occupied by another actual descriptor")
	}
	finalDisk := os.NewFile(8, "original-final-vmm-disk")
	for _, index := range []int{6, 7, 8} {
		if child.extra[index] != nil {
			if err := child.extra[index].Close(); err != nil {
				return err
			}
			child.extra[index] = nil
		}
	}
	if err := sendReadonlyNativeV1(c, vmmReadonlyDiskAdoptedV1, vmmStageInitV1, nil); err != nil {
		return err
	}
	finalRules, err := readonlyFinalRulesV1(c)
	if err != nil {
		return err
	}
	if err := finalRules.enforce(); err != nil {
		return err
	}
	for _, bound := range c.Binds {
		if err := closeVMMCapturedPinV1(bound.FD, bound.Dev, bound.Ino); err != nil {
			return err
		}
	}
	for _, rule := range pinnedRules(c.Rules) {
		if err := closeVMMCapturedPinV1(rule.FD, rule.Dev, rule.Ino); err != nil {
			return err
		}
	}
	oldHelper := *helper
	*helper = -1
	if err := unix.Close(oldHelper); err != nil {
		return err
	}
	if err := checkVMMReadonlyControlParentV1(c); err != nil {
		return err
	}
	lease := os.NewFile(uintptr(c.VMMExecV1.LeaseFD), "original-readonly-init-lease")
	writer := os.NewFile(uintptr(c.VMMExecV1.WriterFD), "original-readonly-init-sentinel")
	var nonce [16]byte
	decoded, err := hex.DecodeString(c.VMMExecV1.ReadonlyNonce)
	if err != nil || len(decoded) != 16 {
		return vmmModeFailureV1("the original read-only init nonce changed")
	}
	copy(nonce[:], decoded)
	if err := supply.WriteVMMReadonlyProjectionV1(context.Background(), writer, program, lease, plan.Program, nonce); err != nil {
		return err
	}
	if _, err := receiveReadonlyParentV1(context.Background(), conn, until, vmmReadonlyExecContinueV1, 0); err != nil {
		return err
	}
	if err := checkVMMReadonlyControlParentV1(c); err != nil {
		return err
	}
	if err := checkReadonlyDumpPolicyV1(); err != nil {
		return err
	}
	if err := cloexecFrom(3); err != nil {
		return err
	}
	for fd := 4; fd <= 8; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
			return err
		}
	}
	if err := replaceReadonlyInputV1(); err != nil {
		return err
	}
	execErr := unix.Exec(plan.Argv[0], plan.Argv, []string{"LANG=C", "LC_ALL=C", "TZ=UTC"})
	runtime.KeepAlive(child)
	runtime.KeepAlive(program)
	runtime.KeepAlive(kernel)
	runtime.KeepAlive(finalDisk)
	runtime.KeepAlive(lease)
	runtime.KeepAlive(writer)
	return stepFailed("cannot execute the original read-only VMM", "Retain the original runtime and native command owners.", execErr)
}

func lockDownReadonlyInitV1(c *setup, rs *ruleset, progs [][]bpf.RawInstruction) error {
	if c.Sub == nil || os.Getpid() != 1 || unix.Gettid() != 1 {
		return vmmModeFailureV1("the final read-only credential transition has another actual init")
	}
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}); err != nil {
		return err
	}
	if err := readonlyAllThreadsPrctlV2(unix.PR_SET_NO_NEW_PRIVS, 1); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	for capability := 0; capability < 64; capability++ {
		err := readonlyAllThreadsPrctlV2(unix.PR_CAPBSET_DROP, uintptr(capability))
		if errors.Is(err, unix.EINVAL) {
			break
		}
		if err != nil {
			return err
		}
	}
	groups := readonlyPreexecGroupsV1(c)
	if err := unix.Setresgid(int(groups[0]), int(groups[1]), int(groups[2])); err != nil {
		return err
	}
	uid, _ := readonlyExecutionIDsV1(c)
	if err := unix.Setresuid(uid, uid, uid); err != nil {
		return err
	}
	if err := dropReadonlyAllThreadCapsV2(); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	if err := rs.enforce(); err != nil {
		return err
	}
	for _, program := range progs {
		if err := loadFilter(program); err != nil {
			return err
		}
	}
	return checkVMMReadonlyControlParentV1(c)
}

// UID0 does not gain an implicit per-thread capability clear from Setresuid.
// Restrict every actual Go runtime thread before B can be created. The pinned
// static helper is built with CGO_ENABLED=0; Go refuses this API when it cannot
// account for non-Go threads. Such a refusal never falls back to one thread.
func readonlyAllThreadsPrctlV2(option, value uintptr) error {
	_, _, errno := syscall.AllThreadsSyscall6(unix.SYS_PRCTL, option, value, 0, 0, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

func dropReadonlyAllThreadCapsV2() error {
	if err := readonlyAllThreadsPrctlV2(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL); err != nil {
		return stepFailed("cannot clear every original init thread's ambient capabilities", "Use the matching static host helper and retain its native command owner.", err)
	}
	header := &unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := new([2]unix.CapUserData)
	var pinned runtime.Pinner
	pinned.Pin(header)
	pinned.Pin(data)
	defer pinned.Unpin()
	// Every kernel call receives PID0 (its own current thread) and reads the
	// same pinned all-zero sets. No capability is retained in another worker.
	//nolint:gosec // The native capset reads these pinned fixed-size zero sets.
	_, _, errno := syscall.AllThreadsSyscall(unix.SYS_CAPSET, uintptr(unsafe.Pointer(header)), uintptr(unsafe.Pointer(&data[0])), 0)
	runtime.KeepAlive(header)
	runtime.KeepAlive(data)
	if errno != 0 {
		return stepFailed("cannot clear every original init thread's capabilities", "Use the matching static host helper and retain its native command owner.", errno)
	}
	return nil
}

func readonlyInputOffsetsV1() error {
	for fd := 3; fd <= 7; fd++ {
		var before unix.Stat_t
		if err := unix.Fstat(fd, &before); err != nil {
			return err
		}
		copyFD, err := unix.Open("/proc/self/fd/"+strconv.Itoa(fd), unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		var actual unix.Stat_t
		var fs unix.Statfs_t
		err = errors.Join(unix.Fstat(copyFD, &actual), unix.Fstatfs(copyFD, &fs))
		if err == nil && (actual.Dev != before.Dev || actual.Ino != before.Ino || actual.Mode != before.Mode || fs.Flags&unix.ST_RDONLY == 0) {
			err = vmmModeFailureV1("the original read-only input projection changed its native inode or mount")
		}
		if err == nil {
			err = unix.Dup3(copyFD, fd, unix.O_CLOEXEC)
		}
		closeErr := unix.Close(copyFD)
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	return nil
}

func readonlyFinalRulesV1(c *setup) (*ruleset, error) {
	rs, err := newRuleset(kernelABI(), NetNone)
	if err != nil {
		return nil, err
	}
	for _, bound := range c.Binds {
		if err := rs.add(bound.FD, bound.access()); err != nil {
			return nil, err
		}
	}
	for _, rule := range pinnedRules(c.Rules) {
		if err := rs.add(rule.FD, rule.Access); err != nil {
			return nil, err
		}
	}
	for _, rule := range c.FileRulesV1 {
		if err := rs.add(rule.FD, rule.Access); err != nil {
			return nil, err
		}
	}
	if err := rs.add(8, Read|Write); err != nil {
		return nil, err
	}
	if err := rs.addPath("/proc", Read); err != nil {
		return nil, err
	}
	return rs, nil
}

func checkReadonlyDiskV1(c *setup, file *os.File, size int64) error {
	uid, gid := readonlyExecutionIDsV1(c)
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0o7777 != 0o600 || st.Uid != uint32(uid) || st.Gid != uint32(gid) || st.Nlink != 1 || st.Size != size || flags&unix.O_ACCMODE != unix.O_RDWR {
		return vmmModeFailureV1("the original init received another subordinate disk capability")
	}
	var header [4]byte
	if _, err := file.ReadAt(header[:], 0); err != nil {
		return err
	}
	if bytes.Equal(header[:], []byte{'Q', 'F', 'I', 0xfb}) {
		return vmmModeFailureV1("the original session disk is not raw")
	}
	return nil
}

func replaceReadonlyInputV1() error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		r.Close()
		return err
	}
	err = unix.Dup3(int(r.Fd()), 0, 0)
	return errors.Join(err, r.Close())
}

func checkReadonlyDumpPolicyV1() (returned error) {
	file, err := os.Open("/proc/sys/fs/suid_dumpable")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, file.Close()) }()
	var fs unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &fs); err != nil {
		return err
	}
	if fs.Type != unix.PROC_SUPER_MAGIC {
		return vmmModeFailureV1("the actual read-only exec policy is not native procfs")
	}
	data, err := io.ReadAll(io.LimitReader(file, 17))
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(data), []byte("0")) {
		return vmmModeFailureV1("the actual kernel policy cannot retain NoDump at read-only exec")
	}
	return nil
}
