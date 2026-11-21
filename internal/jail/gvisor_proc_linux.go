package jail

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func mapGvisorSelfV2() error {
	// The original A producer cleared its actual groups before the first
	// deny. B inherits that deny; it must verify the inherited empty set.
	// If this kernel starts B with allow, B clears before its own first deny.
	groups, err := unix.Getgroups()
	if err != nil || len(groups) != 0 {
		return errors.Join(err, gvisorHandoffFailureV2("the actual inherited supplementary groups are not empty"))
	}
	permission, err := os.ReadFile("/proc/self/setgroups")
	if err != nil {
		return err
	}
	entries := [][2]string{{"uid_map", "0 0 1\n"}}
	if string(permission) == "allow\n" {
		if err := unix.Setgroups(nil); err != nil {
			return err
		}
		groups, err = unix.Getgroups()
		if err != nil || len(groups) != 0 {
			return errors.Join(err, gvisorHandoffFailureV2("the actual supplementary group clear failed"))
		}
		entries = append(entries, [2]string{"setgroups", "deny\n"})
	} else if string(permission) != "deny\n" {
		return gvisorHandoffFailureV2("the inherited setgroups permission is unknown")
	}
	entries = append(entries, [2]string{"gid_map", "0 0 1\n"})
	for _, entry := range entries {
		file, err := os.OpenFile("/proc/self/"+entry[0], os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		n, writeErr := file.WriteString(entry[1])
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil || n != len(entry[1]) {
			return errors.Join(writeErr, closeErr, io.ErrShortWrite)
		}
	}
	// Writing the map gives the already-held kernel identity the literal 0
	// view. No other saved ID or group may become an alternate authority.
	if err := unix.Setresgid(0, 0, 0); err != nil {
		return err
	}
	if err := unix.Setresuid(0, 0, 0); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGKILL), 0, 0, 0); err != nil {
		return err
	}
	if os.Getpid() != 1 || os.Getppid() != 0 {
		return gvisorHandoffFailureV2("the self-map child is not PID1 of its fresh child namespace")
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	actual, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil || actual != 0 {
		return errors.Join(err, gvisorHandoffFailureV2("the actual self-mapped child is dumpable"))
	}
	denied, err := os.ReadFile("/proc/self/setgroups")
	if err != nil || string(denied) != "deny\n" {
		return errors.Join(err, gvisorHandoffFailureV2("the child did not deny supplementary group changes"))
	}
	return nil
}

func openGvisorProcV2(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var fs unix.Statfs_t
	if err = unix.Fstatfs(fd, &fs); err != nil || fs.Type != unix.PROC_SUPER_MAGIC {
		_ = unix.Close(fd)
		return nil, errors.Join(err, gvisorHandoffFailureV2("the ancestry object is not a native proc task"))
	}
	return os.NewFile(uintptr(fd), "gvisor-native-proc-task"), nil
}

// mountGvisorProcV2 replaces A's inherited process view with a fresh procC.
// Every nonprocess top-level entry is then a readonly mount, including all
// globals and nested controls. Only task directories/self/thread-self remain
// writable for the verified runtime's own mapping and oom_score_adj writers.
func mountGvisorProcV2() error {
	if err := mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return err
	}
	if err := mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == "self" || name == "thread-self" {
			continue
		}
		if pid, err := strconv.Atoi(name); err == nil && pid > 0 && strconv.Itoa(pid) == name {
			continue
		}
		path := filepath.Join("/proc", name)
		if err := mount(path, path, "", unix.MS_BIND, ""); err != nil {
			return err
		}
		// This is a new procC with no inherited child mounts. Each
		// non-recursive bind therefore contains only this proc filesystem's
		// tree; a readonly bind protects every descendant without requiring
		// a newer kernel's recursive mount-attribute syscall.
		if err := mount("", path, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
			return err
		}
	}
	stat, err := readGvisorTaskPathV2("/proc/self/stat")
	if err != nil || stat.pid != 1 || stat.parent != 0 {
		return errors.Join(err, gvisorHandoffFailureV2("the mounted proc view includes an ancestor process namespace"))
	}
	return nil
}

// The bootstrap has no setup/private rights yet. Its first exact layer comes
// from the already-verified safe mount graph: each native mount's own locked
// readonly/noexec flags define its rights. The later commandRules layer can
// only narrow these grants after the original descriptor vector arrives.
func confineGvisorBootstrapV2(ownA, parentA *os.File) error {
	rs, err := newRuleset(kernelABI(), NetNone)
	if err != nil {
		return err
	}
	if rs.fd < 0 {
		return rs.enforce()
	}
	defer func() {
		if rs.fd >= 0 {
			_ = unix.Close(rs.fd)
		}
	}()
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	// These locally captured post-ND0 proof objects are not runtime paths.
	// Their exact task directories can be read only during the handshake;
	// every raw alias closes before the private vector is released.
	for _, proof := range []*os.File{ownA, parentA} {
		if err := rs.add(int(proof.Fd()), Read); err != nil {
			return err
		}
	}
	mounts := 0
	for _, line := range strings.Split(string(data), "\n") {
		before, _, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		fields := strings.Fields(before)
		if len(fields) < 6 {
			return gvisorHandoffFailureV2("the child native mount graph is malformed")
		}
		path, err := decodeGvisorMountPathV2(fields[4])
		if err != nil {
			return err
		}
		if path != "/" && !strings.HasPrefix(path, "/") {
			return gvisorHandoffFailureV2("the child mount graph escaped its safe root")
		}
		access := Read
		flags := "," + fields[5] + ","
		if !strings.Contains(flags, ",ro,") {
			access |= Write
		}
		if !strings.Contains(flags, ",noexec,") {
			access |= Exec
		}
		if err := rs.addPath(path, access); err != nil {
			return err
		}
		mounts++
	}
	if mounts == 0 {
		return gvisorHandoffFailureV2("the child has no exact native mount graph")
	}
	err = rs.enforce()
	rs.fd = -1
	return err
}

func decodeGvisorMountPathV2(path string) (string, error) {
	result := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\").Replace(path)
	if strings.ContainsAny(result, "\x00\r\n") || !filepath.IsAbs(result) || filepath.Clean(result) != result {
		return "", gvisorHandoffFailureV2("the native mount target is not canonical")
	}
	return result, nil
}

type gvisorTaskV2 struct {
	pid, parent int
	birth       uint64
}

func parseGvisorTaskV2(data []byte) (gvisorTaskV2, error) {
	var task gvisorTaskV2
	text := string(data)
	open := strings.Index(text, " (")
	close := strings.LastIndex(text, ") ")
	if open < 1 || close <= open || len(data) > 8192 {
		return task, gvisorHandoffFailureV2("the native proc task identity is malformed")
	}
	var err error
	task.pid, err = strconv.Atoi(text[:open])
	if err != nil || task.pid <= 0 || strconv.Itoa(task.pid) != text[:open] {
		return task, gvisorHandoffFailureV2("the native task has no exact positive PID")
	}
	fields := strings.Fields(text[close+2:])
	if len(fields) < 20 || len(fields[0]) != 1 || fields[0] == "Z" || fields[0] == "X" {
		return task, gvisorHandoffFailureV2("the native task ended or has incomplete identity")
	}
	task.parent, err = strconv.Atoi(fields[1])
	if err != nil || task.parent < 0 || strconv.Itoa(task.parent) != fields[1] {
		return task, gvisorHandoffFailureV2("the native task parent identity is invalid")
	}
	task.birth, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil || strconv.FormatUint(task.birth, 10) != fields[19] {
		return task, gvisorHandoffFailureV2("the native task birth identity is invalid")
	}
	return task, nil
}

func readGvisorTaskPathV2(path string) (gvisorTaskV2, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return gvisorTaskV2{}, err
	}
	return parseGvisorTaskV2(data)
}
func readGvisorAtV2(directory *os.File, path string) ([]byte, error) {
	fd, err := unix.Openat(int(directory.Fd()), path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "gvisor-native-proc-value")
	data, readErr := io.ReadAll(io.LimitReader(file, 8193))
	closeErr := file.Close()
	if len(data) > 8192 {
		return nil, gvisorHandoffFailureV2("the native proc value exceeds its fixed bound")
	}
	return data, errors.Join(readErr, closeErr)
}

func verifyGvisorAncestryV2(ownA, localParentA, childA, parentA *os.File, socketFD, sentSocketFD int) error {
	if socketFD != sentSocketFD || socketFD < 3 {
		return gvisorHandoffFailureV2("the socket proof names another child descriptor")
	}
	var own, localParent, child, parent unix.Stat_t
	if err := errors.Join(unix.Fstat(int(ownA.Fd()), &own), unix.Fstat(int(localParentA.Fd()), &localParent), unix.Fstat(int(childA.Fd()), &child), unix.Fstat(int(parentA.Fd()), &parent)); err != nil {
		return err
	}
	if own.Dev != child.Dev || own.Ino != child.Ino || localParent.Dev != parent.Dev || localParent.Ino != parent.Ino || child.Dev != parent.Dev || child.Ino == parent.Ino || child.Mode&unix.S_IFMT != unix.S_IFDIR || parent.Mode&unix.S_IFMT != unix.S_IFDIR {
		return gvisorHandoffFailureV2("the native proof substituted the child or proc superblock")
	}
	for _, file := range []*os.File{ownA, childA, parentA} {
		var fs unix.Statfs_t
		if err := unix.Fstatfs(int(file.Fd()), &fs); err != nil || fs.Type != unix.PROC_SUPER_MAGIC {
			return errors.Join(err, gvisorHandoffFailureV2("the native proof is not on the same process filesystem"))
		}
	}
	childRaw, err := readGvisorAtV2(childA, "stat")
	if err != nil {
		return err
	}
	childTask, err := parseGvisorTaskV2(childRaw)
	if err != nil {
		return err
	}
	parentRaw, err := readGvisorAtV2(parentA, "stat")
	if err != nil {
		return err
	}
	parentTask, err := parseGvisorTaskV2(parentRaw)
	if err != nil {
		return err
	}
	ownC, err := readGvisorTaskPathV2("/proc/self/stat")
	if err != nil {
		return err
	}
	if childTask.pid <= 1 || childTask.parent != 1 || parentTask.pid != 1 || childTask.parent != parentTask.pid || childTask.birth != ownC.birth || ownC.pid != 1 || ownC.parent != 0 {
		return gvisorHandoffFailureV2("the supplied process objects do not prove the original PID1 ancestry and current birth")
	}
	for _, name := range []string{"user", "pid"} {
		a, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(childA.Fd())) + "/ns/" + name)
		if err != nil {
			return err
		}
		current, err := os.Readlink("/proc/self/ns/" + name)
		if err != nil || a != current {
			return errors.Join(err, gvisorHandoffFailureV2("the native child changed its user or PID namespace"))
		}
	}
	var socket unix.Stat_t
	if err := unix.Fstat(socketFD, &socket); err != nil || socket.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return errors.Join(err, gvisorHandoffFailureV2("the child socket proof has another native role"))
	}
	buffer := make([]byte, 256)
	n, err := unix.Readlinkat(int(childA.Fd()), "fd/"+strconv.Itoa(socketFD), buffer)
	if err != nil || string(buffer[:max(n, 0)]) != fmt.Sprintf("socket:[%d]", socket.Ino) {
		return errors.Join(err, gvisorHandoffFailureV2("the proc task does not own this exact fresh socket inode"))
	}
	// Reread both native objects while all proof handles are held. The current
	// birth and ancestry must remain identical throughout the handshake.
	afterRaw, err := readGvisorAtV2(childA, "stat")
	if err != nil {
		return err
	}
	after, err := parseGvisorTaskV2(afterRaw)
	if err != nil || after != childTask {
		return errors.Join(err, gvisorHandoffFailureV2("the original child changed during native proof"))
	}
	return nil
}
