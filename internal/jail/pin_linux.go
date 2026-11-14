package jail

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// bind is a Bind as the stages see it: what Command pinned, and the
// descriptor that holds it.
type bind struct {
	Path  string `json:"path"`
	Write bool   `json:"write,omitempty"`
	Kind  kind   `json:"kind"`
	Dev   uint64 `json:"dev"`
	Ino   uint64 `json:"ino"`
	FD    int    `json:"fd"`
	Real  string `json:"real"` // the path that Command resolved
}

// rule is a PathRule as the stages see it. FD 0 means the stage's own
// /proc/self.
type rule struct {
	Path   string `json:"path"`
	Access Access `json:"access"`
	Dir    bool   `json:"dir,omitempty"`
	FD     int    `json:"fd,omitempty"`
	Real   string `json:"real,omitempty"`
	Dev    uint64 `json:"dev,omitempty"`
	Ino    uint64 `json:"ino,omitempty"`
}

// kind is what a bound path is.
type kind string

const (
	kindDir    kind = "dir"
	kindFile   kind = "file"
	kindDevice kind = "device"
	kindSocket kind = "socket"
)

// access is the Landlock rights that a bind gives: read and run, or read and
// write. A writable path cannot also run files unless a PathRule says so.
func (b bind) access() Access {
	a := Read | Exec
	if b.Write {
		a = Read | Write
	}
	switch b.Kind {
	case kindDevice:
		a |= Ioctl
	case kindSocket:
		a |= Connect
	case kindDir, kindFile:
	}
	return a
}

// pinned holds the descriptors of a jail's binds and rules, in the order
// that its first stage inherits them, after its files, set-up and status.
type pinned struct {
	files []*os.File
	binds []bind
	rules []rule
}

func (p pinned) close() {
	for _, f := range p.files {
		_ = f.Close()
	}
}

// pin opens each bound path once, and each rule's path beneath its bind,
// and keeps the descriptors. The jail then mounts and allows exactly these
// objects, whatever the paths name later. A bound path may pass through
// ordinary symbolic links, but not through /proc's magic links. A rule's
// path may pass through no link at all, and stays beneath its bind.
func pin(c checked, files int) (pinned, error) {
	var p pinned
	fail := func(err error) (pinned, error) { p.close(); return pinned{}, err }
	base := 5 + files // after the files, the set-up and the status
	hide := hiddenPaths()
	if len(c.binds) > 0 && hide.err != nil {
		return fail(pinError("cannot check the host's protected paths",
			"Check the host's home, runtime and device paths, then try again.", hide.err))
	}
	var lines [][]devIno
	for _, b := range c.binds {
		fd, err := openPath(unix.AT_FDCWD, b.Path, unix.RESOLVE_NO_MAGICLINKS)
		if err != nil {
			return fail(pinError("cannot bind "+quote(b.Path),
				"Make the path exist on the host, or leave it out of Spec.Binds.", err))
		}
		p.files = append(p.files, os.NewFile(uintptr(fd), b.Path))
		real, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		if err == nil && strings.HasSuffix(real, " (deleted)") {
			err = errors.New("it was deleted")
		}
		if err != nil {
			return fail(failure("cannot bind "+quote(b.Path)+": "+err.Error(),
				"Make the path exist on the host, or leave it out of Spec.Binds."))
		}
		if why := forbidden(real, hide); why != "" {
			return fail(failure("a jail cannot bind "+quote(b.Path)+", which is "+why,
				"Bind only the folders and files that the command needs."))
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return fail(failure("cannot bind "+quote(b.Path)+": "+err.Error(), "Report this as a bug."))
		}
		line, err := lineage(fd, real)
		if err != nil {
			return fail(pinError("cannot read the folders above "+quote(b.Path),
				"Check that no other program moves the path while jail.Command runs.", err))
		}
		lines = append(lines, line)
		p.binds = append(p.binds, bind{Path: b.Path, Write: b.Write,
			Kind: kindOf(st.Mode), Dev: st.Dev, Ino: st.Ino, FD: base + len(p.files) - 1, Real: real})
	}
	if err := checkAliases(p.binds, lines); err != nil {
		return fail(err)
	}
	if err := checkBackingBindAliases(p.binds, p.files); err != nil {
		return fail(err)
	}
	for _, r := range c.rules {
		if r.Path == procSelf {
			p.rules = append(p.rules, rule{Path: r.Path, Access: r.Access, Dir: true})
			continue
		}
		fd, err := openRule(c.binds, p.files, r.Path)
		if err != nil {
			return fail(pinError("cannot open the Landlock rule's path "+quote(r.Path),
				"Name a path beneath its bind, with no symbolic link on the way.", err))
		}
		p.files = append(p.files, os.NewFile(uintptr(fd), r.Path))
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return fail(failure("cannot open the Landlock rule's path "+quote(r.Path)+": "+err.Error(), "Report this as a bug."))
		}
		real, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		if err != nil || strings.HasSuffix(real, " (deleted)") {
			return fail(failure("cannot keep the Landlock rule's path "+quote(r.Path),
				"Do not change a jail's paths while jail.Command runs."))
		}
		p.rules = append(p.rules, rule{Path: r.Path, Access: r.Access, Dir: st.Mode&unix.S_IFMT == unix.S_IFDIR,
			FD: base + len(p.files) - 1, Real: real, Dev: st.Dev, Ino: st.Ino})
	}
	return p, nil
}

// openRule opens a rule's path beneath the deepest bind that holds it.
func openRule(binds []Bind, fds []*os.File, path string) (int, error) {
	paths := make([]string, len(binds))
	for i, b := range binds {
		paths[i] = b.Path
	}
	at := holder(paths, path)
	if at < 0 {
		return -1, errors.New("no bind holds it")
	}
	return openBeneath(int(fds[at].Fd()), paths[at], path)
}

// holder returns the index of the deepest of paths that holds path, or -1.
func holder(paths []string, path string) int {
	at := -1
	for i, p := range paths {
		if beneath(path, p) && (at < 0 || len(p) > len(paths[at])) {
			at = i
		}
	}
	return at
}

// openBeneath opens path, which is bindPath or beneath it, from dir, the
// descriptor of bindPath, with no link on the way and no way out of it.
func openBeneath(dir int, bindPath, path string) (int, error) {
	rel := strings.TrimPrefix(strings.TrimPrefix(path, bindPath), "/")
	if rel == "" {
		fd, err := unix.FcntlInt(uintptr(dir), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			return -1, err //nolint:wrapcheck // the caller adds the path
		}
		return fd, nil
	}
	return openPath(dir, rel, unix.RESOLVE_BENEATH|unix.RESOLVE_NO_SYMLINKS|unix.RESOLVE_NO_MAGICLINKS)
}

// openat2 is unix.Openat2. A test replaces it to fake an old kernel.
var openat2 = unix.Openat2

// errNoOpenat2 is the error of a kernel before Linux 5.6, which has no
// openat2. No other call resolves a path without /proc's magic links, so a
// jail refuses to pin one there.
var errNoOpenat2 = failure("this kernel has no openat2, and a jail needs it to pin its paths safely",
	"Update the host's kernel to Linux 5.6 or later.")

// openPath opens path with O_PATH, as resolve allows.
func openPath(dir int, path string, resolve uint64) (int, error) {
	fd, err := openat2(dir, path, &unix.OpenHow{Flags: unix.O_PATH | unix.O_CLOEXEC, Resolve: resolve})
	if errors.Is(err, unix.ENOSYS) {
		return -1, errNoOpenat2
	}
	if err != nil {
		return -1, err
	}
	return fd, nil
}

// pinError is the error of a path that cannot be pinned. A kernel without
// openat2 keeps its own message, which names the version to update to.
func pinError(message, hint string, err error) error {
	if errors.Is(err, errNoOpenat2) {
		return err
	}
	return failure(message+": "+err.Error(), hint)
}

// devIno names a file on the host, whatever path leads to it.
type devIno struct{ dev, ino uint64 }

// lineage returns the file that fd holds, then each folder above it up to
// the root. It starts from the file's parent, opened by its path with no
// link on the way, and checks that the parent holds the pinned file under
// its name. So a private folder, such as one that a subuid jail owns with
// mode 0700, has a lineage although the login user cannot search it.
func lineage(fd int, real string) ([]devIno, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err //nolint:wrapcheck // the caller adds the path
	}
	out := []devIno{{st.Dev, st.Ino}}
	cur, err := openPath(unix.AT_FDCWD, filepath.Dir(real), unix.RESOLVE_NO_SYMLINKS|unix.RESOLVE_NO_MAGICLINKS)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(cur) }()
	var child unix.Stat_t
	if err := unix.Fstatat(cur, filepath.Base(real), &child, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err //nolint:wrapcheck // the caller adds the path
	}
	if (devIno{child.Dev, child.Ino}) != out[0] {
		return nil, errors.New("it moved while it was pinned")
	}
	for range 4096 {
		if err := unix.Fstat(cur, &st); err != nil {
			return nil, err //nolint:wrapcheck // the caller adds the path
		}
		di := devIno{st.Dev, st.Ino}
		out = append(out, di)
		up, err := unix.Openat(cur, "..", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err //nolint:wrapcheck // the caller adds the path
		}
		var parent unix.Stat_t
		if err := unix.Fstat(up, &parent); err != nil {
			_ = unix.Close(up)
			return nil, err //nolint:wrapcheck // the caller adds the path
		}
		if (devIno{parent.Dev, parent.Ino}) == di {
			_ = unix.Close(up)
			break // only this folder and its own parent identify the root
		}
		_ = unix.Close(cur)
		cur = up
	}
	return out, nil
}

// checkAliases refuses two binds that pin the same file, and a bind that
// holds another with other rights, by the files that they pin and the
// folders above them. So a link or a second mount that names a bound
// folder's child counts as inside it.
func checkAliases(binds []bind, lines [][]devIno) error {
	for i, a := range binds {
		for j, b := range binds {
			switch {
			case i == j:
			case lines[i][0] == lines[j][0]:
				return failure("the bound paths "+quote(a.Path)+" and "+quote(b.Path)+" name the same file",
					"Bind each file once.")
			case a.Write != b.Write && slices.Contains(lines[j][1:], lines[i][0]):
				return failure("the bound path "+quote(b.Path)+" is inside "+quote(a.Path)+", with other rights",
					"Bind the two paths with the same Write, or bind paths that do not overlap.")
			}
		}
	}
	return nil
}

// reopen opens each bind again, in this stage's mount namespace, and
// checks that it is still the object that Command pinned, at the path that
// Command resolved.
func reopen(binds []bind) ([]int, error) {
	fds := make([]int, 0, len(binds))
	for _, b := range binds {
		fd, err := openPath(unix.AT_FDCWD, b.Path, unix.RESOLVE_NO_MAGICLINKS)
		var st unix.Stat_t
		if err == nil {
			err = unix.Fstat(fd, &st)
		}
		if err == nil && (st.Dev != b.Dev || st.Ino != b.Ino) {
			err = errors.New("it names another file than when jail.Command pinned it")
		}
		if err == nil {
			err = samePath(fd, b.Real)
		}
		if err != nil {
			if fd >= 0 {
				_ = unix.Close(fd)
			}
			for _, f := range fds {
				_ = unix.Close(f)
			}
			return nil, failure("the bound path "+quote(b.Path)+" changed: "+err.Error(),
				"Do not change a jail's bound paths between jail.Command and Start.")
		}
		fds = append(fds, fd)
	}
	return fds, nil
}

// samePath says why the object that fd holds no longer has the path real,
// or nil when it has.
func samePath(fd int, real string) error {
	now, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	if err != nil {
		return err //nolint:wrapcheck // the caller adds the path
	}
	if now != real {
		return errors.New("it moved from " + quote(real) + " to " + quote(now))
	}
	return nil
}

func kindOf(mode uint32) kind {
	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return kindDir
	case unix.S_IFCHR, unix.S_IFBLK:
		return kindDevice
	case unix.S_IFSOCK:
		return kindSocket
	default:
		return kindFile
	}
}

// The calls that cloexecFrom makes. A test replaces them to fake a kernel
// without close_range, or a /proc that cannot be read.
var (
	closeRange = unix.CloseRange
	fdDir      = "/proc/self/fd"
	setCloexec = func(fd int) error {
		_, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, unix.FD_CLOEXEC)
		return err //nolint:wrapcheck // cloexecFrom adds the descriptor
	}
)

// cloexecFrom marks every descriptor from n up as close-on-exec, so that a
// child gets only the descriptors that it is given. Before Linux 5.11, it
// marks each one that /proc/self/fd lists. It fails closed: when it cannot
// mark them all, the stage stops before it starts anything.
func cloexecFrom(n int) error {
	if err := closeRange(uint(n), math.MaxUint32, unix.CLOSE_RANGE_CLOEXEC); err == nil {
		return nil
	}
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return failure("cannot mark this jail stage's descriptors close-on-exec: "+err.Error(),
			"Update the host's kernel to Linux 5.11 or later, which has close_range, or mount /proc.")
	}
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd < n {
			continue
		}
		// The descriptor that read the folder is closed by now: it cannot leak.
		if err := setCloexec(fd); err != nil && !errors.Is(err, unix.EBADF) {
			return failure("cannot mark descriptor "+e.Name()+" close-on-exec: "+err.Error(), "Report this as a bug.")
		}
	}
	return nil
}
