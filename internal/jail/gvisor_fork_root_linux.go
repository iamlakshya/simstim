//go:build linux

package jail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// gvisorForkRootOwnerV2 is the preselected CLOSED original mapped-A mounted-
// root issuer. It is not a successful outside /proc/A/root observation and is
// never selected as a fallback after a denied observation. Actual mapped A
// captures its post-buildRoot root, compares it with its current root at the
// exact versioned report, and outside checks every retained backing role.
type gvisorForkRootOwnerV2 struct {
	mu                     sync.Mutex
	borrowers              sync.WaitGroup
	command                *Cmd
	root                   *os.File
	rootInfo               os.FileInfo
	expected               map[string]*os.File
	ready, closing, closed bool
}

func captureGvisorForkBindingsV2(command *Cmd) (_ *gvisorForkRootOwnerV2, returned error) {
	if command == nil || command.gvisorOuter == nil || command.phase != phaseNew {
		return nil, gvisorHandoffFailureV2("fork mounts have no original pending gvisor Cmd")
	}
	owner := &gvisorForkRootOwnerV2{command: command, expected: make(map[string]*os.File)}
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, owner.close())
		}
	}()
	for _, bound := range command.binds {
		if bound.Path != "/work" && bound.Path != "/home/agent" && !strings.HasPrefix(bound.Path, "/home/agent/") {
			continue
		}
		index := bound.FD - 5 - len(command.files)
		if !bound.Write || !bound.SuppliedV1 || bound.Kind != kindDir || index < 0 || index >= len(command.pins) || owner.expected[bound.Path] != nil {
			return nil, gvisorHandoffFailureV2("a fixed gvisor fork mount lost its original supplied directory role")
		}
		file, err := captureFile(command.pins[index], "original gvisor fork mount", "Retain the original closed gvisor factory and its mount vector.")
		if err != nil {
			return nil, err
		}
		owner.expected[bound.Path] = file
		var stat unix.Stat_t
		if err := unix.Fstat(int(file.Fd()), &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(stat.Dev) != bound.Dev || stat.Ino != bound.Ino {
			return nil, errors.Join(err, gvisorHandoffFailureV2("a retained gvisor fork mount differs from its actual original bind"))
		}
	}
	if owner.expected["/home/agent"] == nil {
		return nil, gvisorHandoffFailureV2("the gvisor factory omitted its actual fresh-home source")
	}
	complete = true
	return owner, nil
}

// captureGvisorMountedRootV2 runs only in original A after its REAL buildRoot.
// Its O_PATH object is private, CLOEXEC and absent from all setup/File vectors.
func captureGvisorMountedRootV2() (_ *os.File, returned error) {
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "original-A-mounted-root")
	if err := validateGvisorMountedRootV2(file); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func validateGvisorMountedRootV2(root *os.File) error {
	if root == nil {
		return gvisorHandoffFailureV2("the original mapped A mounted-root role is absent")
	}
	var stat unix.Stat_t
	var fs unix.Statfs_t
	flags, flagsErr := unix.FcntlInt(root.Fd(), unix.F_GETFL, 0)
	if err := errors.Join(unix.Fstat(int(root.Fd()), &stat), unix.Fstatfs(int(root.Fd()), &fs), flagsErr); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != 0o755 || stat.Uid != uint32(os.Getuid()) || fs.Type != unix.TMPFS_MAGIC || fs.Flags&unix.ST_RDONLY == 0 || flags&unix.O_PATH == 0 || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return gvisorHandoffFailureV2("the original mapped A root is not its readonly native tmpfs directory")
	}
	return nil
}

func checkGvisorMountedRootV2(original *os.File) (returned error) {
	if err := validateGvisorMountedRootV2(original); err != nil {
		return err
	}
	current, err := captureGvisorMountedRootV2()
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, current.Close()) }()
	want, wantErr := original.Stat()
	got, gotErr := current.Stat()
	if wantErr != nil || gotErr != nil || !os.SameFile(want, got) {
		return errors.Join(wantErr, gotErr, gvisorHandoffFailureV2("the original mapped A changed its actual mounted root before report"))
	}
	return nil
}

func (owner *gvisorForkRootOwnerV2) acceptRoot(root *os.File) error {
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed || owner.closing || owner.root != nil || root == nil {
		var closeErr error
		if root != nil {
			closeErr = root.Close()
		}
		return errors.Join(gvisorHandoffFailureV2("the original mounted-root receiver was consumed or ended"), closeErr)
	}
	// Custody precedes all fallible checks. A rejected report grants no fork
	// capability, but its exact FD stays with the actual original native owner.
	owner.root = root
	if err := validateGvisorMountedRootV2(root); err != nil {
		return err
	}
	var err error
	owner.rootInfo, err = root.Stat()
	if err != nil {
		return err
	}
	if err := owner.checkMountsLockedV2(); err != nil {
		return err
	}
	owner.ready = true
	return nil
}

func (owner *gvisorForkRootOwnerV2) checkReadyV2() error {
	if owner == nil {
		return gvisorHandoffFailureV2("the original mapped-root native issuer is absent")
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.ready || owner.closing || owner.closed {
		return gvisorHandoffFailureV2("the original mapped-root native issuer is not live")
	}
	return owner.checkMountsLockedV2()
}

func (owner *gvisorForkRootOwnerV2) checkMountsLockedV2() error {
	if owner.root == nil || owner.rootInfo == nil || owner.closed || owner.command == nil {
		return gvisorHandoffFailureV2("the original mapped-root owner is incomplete")
	}
	current, err := owner.root.Stat()
	if err != nil || !os.SameFile(owner.rootInfo, current) {
		return errors.Join(err, gvisorHandoffFailureV2("the actual original mounted-root object changed"))
	}
	for fixed, expected := range owner.expected {
		fd, err := unix.Openat2(int(owner.root.Fd()), strings.TrimPrefix(fixed, "/"), &unix.OpenHow{Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
		if err != nil {
			return err
		}
		actual := os.NewFile(uintptr(fd), "actual-A-fork-mount")
		want, wantErr := expected.Stat()
		got, gotErr := actual.Stat()
		closeErr := actual.Close()
		if wantErr != nil || gotErr != nil || closeErr != nil || !want.IsDir() || !got.IsDir() || !os.SameFile(want, got) {
			return errors.Join(wantErr, gotErr, closeErr, gvisorHandoffFailureV2("the original A root substituted a work, home or state backing object"))
		}
	}
	return nil
}

// WithGvisorForkRootsV2 borrows only this Cmd's versioned original A-mounted
// root. No root FD or source path escapes; the callback receives fixed roots.
// Physical cleanup joins every borrower outside Cmd.mu before descriptor close.
func (c *Cmd) WithGvisorForkRootsV2(ctx context.Context, use func(work, home *os.Root) error) (returned error) {
	if c == nil || ctx == nil || use == nil {
		return gvisorHandoffFailureV2("fork has no original gvisor root consumer")
	}
	if err := c.CheckNativeLiveV1(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	var owner *gvisorForkRootOwnerV2
	if c.gvisorOuter != nil {
		owner = c.gvisorOuter.fork
	}
	c.mu.Unlock()
	if owner == nil {
		return gvisorHandoffFailureV2("the actual gvisor Cmd has no original mounted-root issuer")
	}
	owner.mu.Lock()
	if !owner.ready || owner.closing || owner.closed || owner.expected["/work"] == nil {
		owner.mu.Unlock()
		return gvisorHandoffFailureV2("the original gvisor fork root is absent, closing or only a probe")
	}
	if err := owner.checkMountsLockedV2(); err != nil {
		owner.mu.Unlock()
		return err
	}
	owner.borrowers.Add(1)
	root := owner.root
	owner.mu.Unlock()
	defer owner.borrowers.Done()
	work, err := openGvisorForkRootV2(root, "work")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, work.Close()) }()
	home, err := openGvisorForkRootV2(root, "home/agent")
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, home.Close()) }()
	if err := c.CheckNativeLiveV1(ctx); err != nil {
		return err
	}
	if err := use(work, home); err != nil {
		return err
	}
	if err := c.CheckNativeLiveV1(ctx); err != nil {
		return err
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.checkMountsLockedV2()
}

func openGvisorForkRootV2(original *os.File, fixed string) (_ *os.Root, returned error) {
	fd, err := unix.Openat2(int(original.Fd()), fixed, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "original-gvisor-fork-root")
	defer func() { returned = errors.Join(returned, file.Close()) }()
	want, err := file.Stat()
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		return nil, err
	}
	got, err := root.Stat(".")
	if err != nil || !os.SameFile(want, got) {
		return nil, errors.Join(err, root.Close(), gvisorHandoffFailureV2("the original gvisor rooted descriptor changed identity"))
	}
	return root, nil
}

func (owner *gvisorForkRootOwnerV2) close() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	if owner.closed {
		owner.mu.Unlock()
		return nil
	}
	owner.closing = true
	owner.mu.Unlock()
	owner.borrowers.Wait()
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.closed {
		return nil
	}
	owner.closed = true
	var result error
	if owner.root != nil {
		result = errors.Join(result, owner.root.Close())
	}
	for _, file := range owner.expected {
		result = errors.Join(result, file.Close())
	}
	return result
}
