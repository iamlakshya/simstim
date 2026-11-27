//go:build linux

package slotlaunch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/iamlakshya/simstim/internal/brand"
	"golang.org/x/sys/unix"
)

func RootPath() string     { return "/var/lib/" + brand.Name }
func LauncherPath() string { return "/usr/local/libexec/" + brand.Name + "/" + brand.LaunchBinary }

type caller struct {
	uid, gid int
	name     string
}
type backend struct {
	root   *os.Root
	owner  int
	caller caller
	guard  *os.File
}

// nativeCaller accepts only a real root invocation under sudo's checked local
// account. The wire format has no way to choose or override this identity.
func nativeCaller() (caller, error) {
	if os.Getuid() != 0 || os.Geteuid() != 0 || len(os.Args) != 1 {
		return caller{}, refused("the launcher needs a root sudo invocation with no arguments")
	}
	uid, err := strconv.Atoi(os.Getenv("SUDO_UID"))
	if err != nil || !validAccountID(uid) || strconv.Itoa(uid) != os.Getenv("SUDO_UID") {
		return caller{}, refused("the launcher has no canonical sudo caller uid")
	}
	name := os.Getenv("SUDO_USER")
	data, err := os.ReadFile("/etc/passwd")
	if err != nil || len(data) > 1<<20 {
		return caller{}, refused("the local account database cannot be checked")
	}
	var found *caller
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) != 7 {
			continue
		}
		id, e := strconv.Atoi(parts[2])
		if e != nil {
			continue
		}
		if id != uid && parts[0] != name {
			continue
		}
		gid, e := strconv.Atoi(parts[3])
		if found != nil || id != uid || parts[0] != name || e != nil || !validAccountID(gid) {
			return caller{}, refused("the sudo account is absent or ambiguous in the local account database")
		}
		found = &caller{uid: uid, gid: gid, name: name}
	}
	if found == nil {
		return caller{}, refused("the sudo caller is not a local login account")
	}
	return *found, nil
}

func openBackend(who caller) (*backend, error) {
	root, err := openOwnedRoot(RootPath(), 0)
	if err != nil {
		return nil, err
	}
	b := &backend{root: root, owner: 0, caller: who}
	guard, err := b.open("root-steps/.lock", unix.O_RDONLY, 0)
	if err != nil {
		root.Close()
		return nil, err
	}
	st, err := guard.Stat()
	if err != nil || !singleRegular(st, 0) || st.Mode().Perm()&0o022 != 0 {
		guard.Close()
		root.Close()
		return nil, refused("the root-step administration lock is unsafe")
	}
	if err := unix.Flock(int(guard.Fd()), unix.LOCK_SH|unix.LOCK_NB); err != nil {
		guard.Close()
		root.Close()
		return nil, refused("a root step or undo is running; retry after it ends")
	}
	b.guard = guard
	for _, dir := range []string{"store", "store/sha256", "store/incoming", "slots", "sessions", "vms"} {
		file, err := b.open(dir, unix.O_RDONLY|unix.O_DIRECTORY, 0)
		if err != nil {
			guard.Close()
			root.Close()
			return nil, err
		}
		st, err := file.Stat()
		file.Close()
		if err != nil || !owned(st, 0) || st.Mode().Perm()&0o022 != 0 {
			guard.Close()
			root.Close()
			return nil, refused("the launcher installation contains an unsafe directory")
		}
	}
	return b, nil
}

func owned(st os.FileInfo, uid int) bool {
	stat, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == uid && stat.Nlink > 0
}
func singleRegular(st os.FileInfo, uid int) bool {
	stat, ok := st.Sys().(*syscall.Stat_t)
	return st.Mode().IsRegular() && ok && int(stat.Uid) == uid && stat.Nlink == 1
}

// openOwnedRoot checks every ancestor without following symlinks. A later
// rename of a path cannot change the pinned os.Root used for all operations.
func openOwnedRoot(path string, uid int) (*os.Root, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, refused("the launcher root is not an absolute clean path")
	}
	file, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	defer func() { file.Close() }()
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		fd, e := unix.Openat(int(file.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if e != nil {
			return nil, refused("a launcher root ancestor is missing or is a symbolic link")
		}
		next := os.NewFile(uintptr(fd), part)
		file.Close()
		file = next
		st, e := file.Stat()
		if e != nil || !owned(st, uid) || st.Mode().Perm()&0o022 != 0 {
			return nil, refused("a launcher root ancestor is writable or has the wrong owner")
		}
	}
	return os.OpenRoot("/proc/self/fd/" + strconv.Itoa(int(file.Fd())))
}

func (b *backend) open(name string, flags int, mode uint32) (*os.File, error) {
	base, err := b.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer base.Close()
	fd, err := unix.Openat2(int(base.Fd()), name, &unix.OpenHow{Flags: uint64(flags | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK), Mode: uint64(mode), Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, fmt.Errorf("open protected launcher object: %w", err)
	}
	return os.NewFile(uintptr(fd), name), nil
}

func temporary(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return prefix + "." + hex.EncodeToString(random[:]), nil
}

func (b *backend) syncDir(name string) error {
	file, err := b.open(name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func (b *backend) writeJSON(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := temporary(name)
	if err != nil {
		return err
	}
	file, err := b.open(tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer b.root.Remove(tmp)
	_, err = file.Write(append(data, '\n'))
	if err == nil {
		err = file.Sync()
	}
	err = joinClose(err, file)
	if err != nil {
		return err
	}
	if err := b.root.Rename(tmp, name); err != nil {
		return err
	}
	return b.syncDir(filepath.Dir(name))
}

func joinClose(err error, file *os.File) error {
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (b *backend) readJSON(name string, target any) error {
	file, err := b.open(name, unix.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil || !singleRegular(st, b.owner) || st.Mode().Perm()&0o022 != 0 {
		return refused("a launcher record is not a protected regular file")
	}
	dec := json.NewDecoder(io.LimitReader(file, RequestMax+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return refused("a launcher record is malformed")
	}
	if dec.Decode(new(any)) != io.EOF {
		return refused("a launcher record contains trailing data")
	}
	return nil
}

func (b *backend) lock(name string, nonblocking bool) (*os.File, error) {
	file, err := b.open(name, unix.O_RDONLY|unix.O_CREAT, 0o644)
	if err != nil {
		return nil, err
	}
	st, err := file.Stat()
	if err != nil || !singleRegular(st, b.owner) || st.Mode().Perm()&0o022 != 0 {
		file.Close()
		return nil, refused("a launcher lock is not protected")
	}
	flags := unix.LOCK_EX
	if nonblocking {
		flags |= unix.LOCK_NB
	}
	if err := unix.Flock(int(file.Fd()), flags); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
