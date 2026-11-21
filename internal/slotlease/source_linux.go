//go:build linux

// Package slotlease captures the original root allocation in its actual
// credential-dropped keeper. It accepts no caller-supplied fd, path or policy.
package slotlease

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"golang.org/x/sys/unix"
)

type cgroupIdentity struct {
	Path      string `json:"path"`
	Dev       uint64 `json:"dev"`
	Ino       uint64 `json:"ino"`
	ParentDev uint64 `json:"parent_dev"`
	ParentIno uint64 `json:"parent_ino"`
}
type allocation struct {
	Slot    int             `json:"slot"`
	Session string          `json:"session"`
	UID     int             `json:"uid"`
	GID     int             `json:"gid"`
	SlotUID int             `json:"slot_uid"`
	SlotGID int             `json:"slot_gid"`
	BootID  string          `json:"boot_id"`
	Cgroup  *cgroupIdentity `json:"cgroup,omitempty"`
}

// SourceV1 has no data constructor. Only CaptureKeeperV1 can mint it from
// the actual original root handoff. Its zero value grants no lease.
type SourceV1 struct {
	mu                    sync.Mutex
	lock, control, cgroup *os.File
	pid, uid, gid         int
	slot                  int
	session, boot         string
	identity              cgroupIdentity
	closed                bool
}

func failure(message string) error {
	return fmt.Errorf("%s\nTry: retain the original slot allocation and run the host doctor.", message)
}

func duplicate(fd int, name string) (*os.File, error) {
	owned, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(owned), name), nil
}

// CaptureKeeperV1 owns duplicates of fixed handoff descriptors 5, 6 and 12.
// Root alone created the protected listener. The final VMM never inherits
// any of these descriptors, so a request or a restarted process cannot mint
// the same native handoff from saved identifiers.
func CaptureKeeperV1() (_ *SourceV1, returned error) {
	state, err := secretmem.Inspect()
	if err != nil || state.Dumpable == nil || *state.Dumpable != 0 || state.CoreSoft != 0 || state.CoreHard != 0 {
		return nil, failure("the slot keeper is not protected before its native capture")
	}
	s := &SourceV1{pid: os.Getpid(), uid: os.Getuid(), gid: os.Getgid(), slot: -1}
	defer func() {
		if returned != nil {
			s.Close()
		}
	}()
	if s.uid <= 0 || os.Geteuid() != s.uid {
		return nil, failure("the slot keeper still has root credentials")
	}
	s.lock, err = duplicate(5, "original-slot-lease")
	if err != nil {
		return nil, err
	}
	s.control, err = duplicate(6, "original-root-listener")
	if err != nil {
		return nil, err
	}
	s.cgroup, err = duplicate(12, "original-slot-cgroup")
	if err != nil {
		return nil, err
	}
	rootPath := "/var/lib/" + brand.Name
	link, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(s.lock.Fd())))
	if err != nil {
		return nil, err
	}
	for i := range 16 {
		if link == fmt.Sprintf("%s/slots/%02d.lock", rootPath, i) {
			s.slot = i
		}
	}
	if s.slot < 0 {
		return nil, failure("the original slot lease is outside the fixed root store")
	}
	var record allocation
	if err := readRecord(fmt.Sprintf("%s/slots/%02d.json", rootPath, s.slot), &record); err != nil {
		return nil, err
	}
	if record.Slot != s.slot || !api.ValidSessionID(record.Session) || record.UID <= 0 || record.GID <= 0 || record.SlotGID != s.gid || record.SlotUID != s.uid || record.Cgroup == nil {
		return nil, failure("the root allocation does not name this native keeper")
	}
	s.session, s.boot, s.identity = record.Session, record.BootID, *record.Cgroup
	if err := s.check(); err != nil {
		return nil, err
	}
	return s, nil
}

func openProtected(name string) (*os.File, error) {
	root, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(root)
	fd, err := unix.Openat2(root, strings.TrimPrefix(name, "/"), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	st, err := file.Stat()
	if err != nil || !rootOwned(st) || !st.Mode().IsRegular() || st.Mode().Perm()&0o022 != 0 {
		file.Close()
		return nil, failure("the slot allocation is not a protected root record")
	}
	return file, nil
}

func readRecord(name string, target any) error {
	file, err := openProtected(name)
	if err != nil {
		return err
	}
	defer file.Close()
	return decodeRecord(file, target)
}

func decodeRecord(input io.Reader, target any) error {
	raw, err := io.ReadAll(io.LimitReader(input, 65537))
	if err != nil || len(raw) > 65536 {
		return failure("the root slot allocation exceeds its protected bound")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(target) != nil || dec.Decode(new(any)) != io.EOF {
		return failure("the root slot allocation is malformed")
	}
	return nil
}

func rootOwned(st os.FileInfo) bool {
	if st == nil {
		return false
	}
	native, ok := st.Sys().(*syscall.Stat_t)
	return ok && native.Uid == 0 && native.Nlink == 1
}

func (s *SourceV1) check() error {
	if s == nil || s.closed || s.lock == nil || s.control == nil || s.cgroup == nil || s.pid != os.Getpid() || s.uid != os.Getuid() || s.gid != os.Getgid() || os.Geteuid() != s.uid {
		return failure("the slot lease has no original current keeper")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(boot)) != s.boot || len(s.boot) != 36 {
		return failure("the root allocation belongs to another native boot")
	}
	st, err := s.lock.Stat()
	if err != nil || !rootOwned(st) || !st.Mode().IsRegular() || st.Mode().Perm() != 0o644 {
		return failure("the original slot lock is no longer protected")
	}
	flags, err := unix.FcntlInt(s.lock.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return failure("the original slot lease is not read-only")
	}
	if err := unix.Flock(int(s.lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return failure("the keeper does not possess the original allocated lock description")
	}
	rootPath := "/var/lib/" + brand.Name
	pathInfo, err := os.Lstat(fmt.Sprintf("%s/slots/%02d.lock", rootPath, s.slot))
	if err != nil || !os.SameFile(st, pathInfo) {
		return failure("the original slot lock inode was replaced")
	}
	address, err := unix.Getsockname(int(s.control.Fd()))
	local, ok := address.(*unix.SockaddrUnix)
	if err != nil || !ok || local.Name != rootPath+"/sessions/"+s.session+".sock" {
		return failure("the keeper lacks its original root control listener")
	}
	kind, err := unix.GetsockoptInt(int(s.control.Fd()), unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || kind != unix.SOCK_STREAM {
		return failure("the original root control descriptor is not a stream listener")
	}
	accepting, err := unix.GetsockoptInt(int(s.control.Fd()), unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	if err != nil || accepting != 1 {
		return failure("the original root control descriptor is not listening")
	}
	st, err = os.Lstat(local.Name)
	if err != nil || !rootOwned(st) || st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0o600 {
		return failure("the root control endpoint is no longer protected")
	}
	st, err = s.cgroup.Stat()
	if err != nil {
		return err
	}
	native, ok := st.Sys().(*syscall.Stat_t)
	if !ok || native.Uid != 0 || uint64(native.Dev) != s.identity.Dev || native.Ino != s.identity.Ino || !st.IsDir() || st.Mode().Perm()&0o022 != 0 {
		return failure("the original slot cgroup identity changed")
	}
	var filesystem unix.Statfs_t
	if unix.Fstatfs(int(s.cgroup.Fd()), &filesystem) != nil || filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return failure("the slot lease is not bound to a native cgroup2 object")
	}
	if err := checkCgroupParent(s.cgroup, s.identity); err != nil {
		return err
	}
	if !strings.HasPrefix(s.identity.Path, "/") || path.Clean(s.identity.Path) != s.identity.Path {
		return failure("the original slot cgroup path is invalid")
	}
	actual, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return err
	}
	matched := false
	for _, line := range strings.Split(string(actual), "\n") {
		if line == "0::"+s.identity.Path {
			matched = true
		}
	}
	if !matched {
		return failure("the native keeper left its original root-created session cgroup")
	}
	return nil
}

// CheckCurrentV1 rechecks the original issuing keeper and native objects. It
// grants no UID, command, cgroup migration or replacement-source permission.
func (s *SourceV1) CheckCurrentV1(ctx context.Context) error {
	if ctx == nil {
		return failure("the slot lease has no owner context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil {
		return failure("the slot lease is missing")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.check()
}

// RetainForInitV1 duplicates the SAME original open file description. The
// consumer must keep it out of final executable files and release only its
// own duplicate. Whole-slot vacancy remains the root allocator's obligation.
func (s *SourceV1) RetainForInitV1() (*os.File, error) {
	if s == nil {
		return nil, failure("the slot lease is missing")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(); err != nil {
		return nil, err
	}
	return duplicate(int(s.lock.Fd()), "slot-init-lease")
}

func (s *SourceV1) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	var result error
	for _, file := range []*os.File{s.lock, s.control, s.cgroup} {
		if file != nil {
			result = errors.Join(result, file.Close())
		}
	}
	return result
}
