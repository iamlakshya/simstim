package jail

import (
	"errors"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

type resolvedTarget struct {
	parentFD int
	base     string
	dstFD    int
	path     string
}

func (t resolvedTarget) close() {
	_ = unix.Close(t.dstFD)
	_ = unix.Close(t.parentFD)
}

type targetWalk struct {
	root    int
	dev     uint64
	path    string
	wantDir bool
	create  bool
	stack   []targetFrame
	queue   []string
	links   int
	trace   bool
	visits  []string
	lookups []string
}

type targetFrame struct {
	fd   int
	name string
	stat unix.Stat_t
}

var errMissingMountTarget = errors.New("the path does not exist inside a bound host folder")
var errMountTargetKind = errors.New("the target has the wrong file kind")
var errExecutableTargetChanged = errors.New("the executable target does not yet name its pinned object")

type targetFailure struct {
	detail error
	cause  error
}

func (e *targetFailure) Error() string   { return e.detail.Error() }
func (e *targetFailure) Unwrap() []error { return []error{e.detail, e.cause} }

// resolveTarget expands ordinary links inside root, including absolute
// links, and clamps .. there. Only the fresh tmpfs can gain new entries.
func resolveTarget(root int, dev uint64, path string, wantDir, create bool) (resolvedTarget, error) {
	w := &targetWalk{root: root, dev: dev, path: path, wantDir: wantDir, create: create,
		queue: strings.Split(strings.TrimPrefix(path, "/"), "/")}
	if err := w.reset(); err != nil {
		return resolvedTarget{}, err
	}
	defer w.close()
	return w.resolve()
}

// traceTarget uses the same rooted component walk without creating anything.
// Missing components are virtual until a later parent bind supplies them.
func traceTarget(root int, dev uint64, path string, wantDir bool) (string, error) {
	trace, err := traceMountTarget(root, dev, path, wantDir)
	return trace.path, err
}

type mountTrace struct {
	path        string
	prefixes    []string
	providers   []int
	problem     error
	entries     []string
	transparent []int
}

func traceMountTarget(root int, dev uint64, path string, wantDir bool) (mountTrace, error) {
	w := &targetWalk{root: root, dev: dev, path: path, wantDir: wantDir, trace: true,
		queue: strings.Split(strings.TrimPrefix(path, "/"), "/")}
	if err := w.reset(); err != nil {
		return mountTrace{}, err
	}
	defer w.close()
	target, err := w.resolve()
	return mountTrace{path: target.path, prefixes: w.lookups, entries: w.visits}, err
}

func (w *targetWalk) close() {
	for _, frame := range w.stack {
		if frame.fd >= 0 {
			_ = unix.Close(frame.fd)
		}
	}
	w.stack = nil
}

func (w *targetWalk) reset() error {
	w.close()
	fd, err := unix.FcntlInt(uintptr(w.root), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return w.failed(err)
	}
	w.stack = []targetFrame{{fd: fd}}
	return nil
}

func (w *targetWalk) failed(err error) error {
	return &targetFailure{detail: stepFailed("cannot make the mount target "+quote(w.path)+" in the jail",
		"Keep bound paths unchanged during start, and create missing paths inside host binds first.", err), cause: err}
}

func (w *targetWalk) resolve() (resolvedTarget, error) {
	for len(w.queue) > 0 {
		lastPart := w.queue[len(w.queue)-1]
		if !w.wantDir && (lastPart == "" || lastPart == "." || lastPart == "..") {
			return resolvedTarget{}, w.failed(unix.ENOTDIR)
		}
		part := w.queue[0]
		w.queue = w.queue[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			if len(w.stack) > 1 {
				if fd := w.stack[len(w.stack)-1].fd; fd >= 0 {
					_ = unix.Close(fd)
				}
				w.stack = w.stack[:len(w.stack)-1]
			}
			continue
		}
		last := len(w.queue) == 0
		if w.trace {
			w.visits = append(w.visits, w.pathFor(part))
			w.lookups = append(w.lookups, w.directoryPath())
		}
		fd, st, err := w.open(part, last)
		if err != nil {
			return resolvedTarget{}, err
		}
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			if err := w.follow(fd); err != nil {
				return resolvedTarget{}, err
			}
			continue
		}
		if !last && st.Mode&unix.S_IFMT != unix.S_IFDIR {
			if fd >= 0 {
				_ = unix.Close(fd)
			}
			if !w.trace {
				return resolvedTarget{}, w.failed(unix.ENOTDIR)
			}
			// A pending directory bind may replace this prefix. Planning
			// keeps its name, then checks the actual kind when mounting.
			fd, st = -1, unix.Stat_t{Mode: unix.S_IFDIR}
		}
		w.stack = append(w.stack, targetFrame{fd: fd, name: part, stat: st})
	}
	if len(w.stack) == 1 {
		return resolvedTarget{}, failure("a jail cannot mount a path on its root", "Name a file or folder beneath the jail's root.")
	}
	frame := w.stack[len(w.stack)-1]
	w.stack = w.stack[:len(w.stack)-1]
	return w.finish(frame.fd, frame.name, frame.stat)
}

func (w *targetWalk) finish(fd int, part string, st unix.Stat_t) (resolvedTarget, error) {
	path := w.pathFor(part)
	if w.trace {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return resolvedTarget{path: path}, nil
	}
	if (st.Mode&unix.S_IFMT == unix.S_IFDIR) != w.wantDir {
		_ = unix.Close(fd)
		return resolvedTarget{}, w.failed(errMountTargetKind)
	}
	parent, err := unix.FcntlInt(uintptr(w.stack[len(w.stack)-1].fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		_ = unix.Close(fd)
		return resolvedTarget{}, w.failed(err)
	}
	return resolvedTarget{parentFD: parent, base: part, dstFD: fd, path: path}, nil
}

func (w *targetWalk) pathFor(part string) string {
	parent := w.directoryPath()
	if parent == "/" {
		return parent + part
	}
	return parent + "/" + part
}

func (w *targetWalk) directoryPath() string {
	components := make([]string, 0, len(w.stack))
	for _, frame := range w.stack[1:] {
		components = append(components, frame.name)
	}
	return "/" + strings.Join(components, "/")
}

func (w *targetWalk) open(part string, last bool) (int, unix.Stat_t, error) {
	cur := w.stack[len(w.stack)-1].fd
	var parent, st unix.Stat_t
	if w.trace && cur < 0 {
		return -1, unix.Stat_t{Mode: unix.S_IFDIR}, nil
	}
	if err := unix.Fstat(cur, &parent); err != nil {
		return -1, st, w.failed(err)
	}
	if parent.Mode&unix.S_IFMT != unix.S_IFDIR {
		return -1, st, w.failed(unix.ENOTDIR)
	}
	fd, err := openat2(cur, part, &unix.OpenHow{Flags: unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS})
	if errors.Is(err, unix.ENOENT) {
		if w.trace {
			return -1, unix.Stat_t{Mode: unix.S_IFDIR}, nil
		}
		fd, err = w.make(cur, part, !last || w.wantDir, parent.Dev)
	}
	if err == nil {
		err = unix.Fstat(fd, &st)
	}
	if err != nil {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return -1, st, w.failed(err)
	}
	return fd, st, nil
}

func (w *targetWalk) make(parent int, part string, dir bool, dev uint64) (int, error) {
	if !w.create || dev != w.dev {
		return -1, errMissingMountTarget
	}
	var err error
	if dir {
		err = unix.Mkdirat(parent, part, dirMode)
	} else {
		var fd int
		fd, err = unix.Openat(parent, part, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_CLOEXEC, 0o600)
		if err == nil {
			_ = unix.Close(fd)
		}
	}
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, err //nolint:wrapcheck // open adds the full target and hint
	}
	fd, err := openPath(parent, part, unix.RESOLVE_BENEATH|unix.RESOLVE_NO_SYMLINKS|unix.RESOLVE_NO_MAGICLINKS)
	var st unix.Stat_t
	if err == nil {
		err = unix.Fstat(fd, &st)
	}
	if err == nil && st.Dev != w.dev {
		err = errors.New("the created target left the jail's tmpfs")
	}
	if err != nil && fd >= 0 {
		_ = unix.Close(fd)
		fd = -1
	}
	return fd, err
}

func (w *targetWalk) follow(fd int) error {
	defer func() { _ = unix.Close(fd) }()
	w.links++
	if w.links > 40 {
		return w.failed(unix.ELOOP)
	}
	var link string
	for n := 4096; n <= 65536; n *= 2 {
		buf := make([]byte, n)
		got, err := unix.Readlinkat(fd, "", buf)
		if err != nil {
			return w.failed(err)
		}
		if got < len(buf) {
			link = string(buf[:got])
			break
		}
	}
	if link == "" {
		return w.failed(unix.ENAMETOOLONG)
	}
	if strings.HasPrefix(link, "/") {
		if err := w.reset(); err != nil {
			return err
		}
	}
	w.queue = append(strings.Split(strings.TrimPrefix(link, "/"), "/"), w.queue...)
	return nil
}

func mountFDPath(fd int) string { return "/oldroot/proc/self/fd/" + strconv.Itoa(fd) }
