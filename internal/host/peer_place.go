package host

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
)

// Where a process runs, from its cgroup file: for the peer check of the
// session sockets (holder_peer.go), and for the helper's own entry points
// that act on a session (CheckCaller).

// peer is the process at the other end of a connection.
type peer struct {
	pid int
	uid int
	// alive reports whether the process that connected still lives, so
	// that no other process has its pid. It is nil when the kernel gives
	// no pidfd.
	alive func() bool
	// done closes the pidfd, when there is one.
	done func()
}

// readPeer is peerOf. Tests replace it.
var readPeer = peerOf

// peerPlace is where a peer process runs.
type peerPlace uint8

const (
	// placeOutside is outside every unit and scope of the sessions' slice.
	placeOutside peerPlace = iota
	// placeOwn is in this session's own unit, or below it.
	placeOwn
	// placeInside is in another unit or scope of the sessions' slice, or
	// in a place that the holder cannot read or trust. All count as inside.
	placeInside
)

// cgroupPaths reads a /proc/<pid>/cgroup file. Each line is
// "id:controllers:path", with id 0 for cgroup v2. It returns each record's
// path, split into its folders, and whether any record has a path below the
// root.
func cgroupPaths(cgroup []byte) (paths [][]string, belowRoot bool) {
	for _, line := range strings.Split(string(cgroup), "\n") {
		_, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		_, path, ok := strings.Cut(rest, ":")
		if !ok || !strings.HasPrefix(path, "/") {
			continue
		}
		belowRoot = belowRoot || path != "/"
		paths = append(paths, strings.Split(path, "/"))
	}
	return paths, belowRoot
}

// placeAmong says where a process with the cgroup paths runs, for the
// session whose unit is unit. A path with the sessions' slice in it is
// inside. The process is in the session's own unit only when, in every such
// path, the folder right after the first slice is unit. A program in
// session B may make a child group named after A's unit inside B's unit,
// and that stays B.
func placeAmong(paths [][]string, unit string) peerPlace {
	inside, own := false, true
	for _, dirs := range paths {
		i := slices.Index(dirs, brand.Slice)
		if i < 0 {
			continue
		}
		inside = true
		own = own && i+1 < len(dirs) && dirs[i+1] == unit
	}
	switch {
	case !inside:
		return placeOutside
	case own:
		return placeOwn
	}
	return placeInside
}

// placeIn says where a peer runs, from its /proc/<pid>/cgroup file, for the
// session whose unit is unit. cgroup v1 shows "/" for a process that is
// exiting, whatever its group was, and an unused v2 hierarchy shows "0::/"
// for every process. So a file with no path below the root says nothing,
// and the peer counts as inside, as with an empty or unreadable file. A live
// process on a host with a systemd user manager, the only host where the
// peer check is on, always has such a path.
func placeIn(cgroup []byte, unit string) peerPlace {
	paths, belowRoot := cgroupPaths(cgroup)
	if !belowRoot {
		return placeInside
	}
	return placeAmong(paths, unit)
}

// SelfCgroup reads this process's own cgroup file. Tests replace it, through
// hosttest.InSession, to run as a program inside a session.
var SelfCgroup = func() ([]byte, error) {
	return os.ReadFile(filepath.Join(procRoot, "self", "cgroup"))
}

// selfCgroupRequired is true where a process always has a cgroup file to
// read: on Linux. Tests set it to check the other case.
var selfCgroupRequired = runtime.GOOS == "linux"

// CgroupNamespaces returns the cgroup namespace of this process, and that of
// the user's systemd user manager, or "" when the user has no manager. The
// manager is the peer of its private socket, at its fixed place under
// /run/user, whatever this process's environment says. Tests replace it.
var CgroupNamespaces = func() (self, manager string, err error) {
	self, err = os.Readlink(filepath.Join(procRoot, "self", "ns", "cgroup"))
	if err != nil {
		return "", "", fmt.Errorf("read this process's cgroup namespace: %w", err)
	}
	sock := fmt.Sprintf("/run/user/%d/systemd/private", os.Getuid())
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(context.Background(), "unix", sock)
	if errors.Is(err, os.ErrNotExist) {
		return self, "", nil
	}
	if err != nil {
		return self, "", fmt.Errorf("reach the user manager: %w", err)
	}
	defer conn.Close()
	p, err := peerOf(conn)
	if err != nil {
		return self, "", fmt.Errorf("read the user manager's process: %w", err)
	}
	if p.done != nil {
		defer p.done()
	}
	manager, err = os.Readlink(filepath.Join(procRoot, strconv.Itoa(p.pid), "ns", "cgroup"))
	if err != nil {
		return self, "", fmt.Errorf("read the user manager's cgroup namespace: %w", err)
	}
	return self, manager, nil
}

// The helper's refusals to act on a session, each with the code forbidden.
var (
	errCallerInside = &proto.Error{Code: "forbidden",
		Message: "the helper cannot act on a session from inside a session",
		Hint:    "Run it from outside every session, such as from the client on your laptop."}
	errCallerNamespace = &proto.Error{Code: "forbidden",
		Message: "the helper runs in a cgroup namespace other than the user manager's, so it cannot tell whether it runs inside a session",
		Hint:    "Run it from a login shell outside every session, or from the client on your laptop."}
)

// CheckCaller refuses to act on a session when this process runs inside a
// session: in a session's unit or a git scope, under the sessions' slice.
// Every entry point of the helper that deletes, resumes, restores, sends to,
// kills or renames a session calls it first, the bridge too.
//
// On Linux, a cgroup file that cannot be read or parsed counts as inside,
// as a peer's does. A cgroup namespace can show a process in a session's
// unit at "/": so where the user has a systemd user manager, and sessions
// run in units, this process must share the manager's cgroup namespace, and
// a file of roots only refuses too. Without a manager, there are no units.
// Off Linux, there are no units either, and it passes.
func CheckCaller() error {
	b, err := SelfCgroup()
	if err != nil {
		if selfCgroupRequired {
			return errCallerInside
		}
		return nil
	}
	paths, belowRoot := cgroupPaths(b)
	if len(paths) == 0 || placeAmong(paths, "") != placeOutside {
		return errCallerInside
	}
	if !selfCgroupRequired {
		return nil
	}
	self, manager, err := CgroupNamespaces()
	switch {
	case err != nil:
		return errCallerNamespace
	case manager == "":
		return nil
	case self != manager:
		return errCallerNamespace
	case !belowRoot:
		return errCallerInside
	}
	return nil
}

// inOwnUnit reports whether this process runs in the unit that launchHolder
// started for session id, under the sessions' slice. It reads cgroup v1 and
// v2 alike. Without a systemd user manager, a holder has no such unit.
func inOwnUnit(id string) bool {
	b, err := SelfCgroup()
	if err != nil {
		return false
	}
	paths, _ := cgroupPaths(b)
	return len(paths) > 0 && placeAmong(paths, UnitName(id)) == placeOwn
}

// placeOf says where the peer of conn runs. It reads the peer's pid and
// uid, then its cgroup. A peer of another user, root included, counts as
// inside. Where the kernel gives a pidfd, it then checks that the same
// process still lives, so a pid that another process took in between
// cannot pass.
func (h *Holder) placeOf(conn net.Conn) peerPlace {
	p, err := readPeer(conn)
	if err != nil {
		return placeInside
	}
	if p.done != nil {
		defer p.done()
	}
	if p.pid <= 0 || p.uid != os.Getuid() {
		return placeInside
	}
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(p.pid), "cgroup"))
	if err != nil {
		return placeInside
	}
	place := placeIn(b, UnitName(h.id))
	if p.alive != nil && !p.alive() {
		return placeInside
	}
	return place
}

// pidfdAnswer says what the answer of getsockopt(SO_PEERPIDFD) means. A
// kernel before Linux 6.5 does not know the option, and answers ENOPROTOOPT:
// the check works there without a pidfd. Any other error, such as EINVAL for
// a peer that has gone, ENODATA or EMFILE, refuses the peer.
func pidfdAnswer(err error) (pidfd, refuse bool) {
	switch {
	case err == nil:
		return true, false
	case errors.Is(err, syscall.ENOPROTOOPT):
		return false, false
	}
	return false, true
}

// pidfdAlive says what the answer of pidfd_send_signal(pidfd, 0) means.
// Only ESRCH says that the process has gone. EPERM, for one, says that it
// lives.
func pidfdAlive(err error) bool { return !errors.Is(err, syscall.ESRCH) }
