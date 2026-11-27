//go:build linux

package nativememory

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

type currentLinux struct {
	proc, self *nativeFile
	mount      memoryMount
	member     string
	groups     []*nativeFile // visible hierarchy root first, current leaf last
}

// CaptureCurrent pins actual current-process kernel accounting. The caller
// owns Close and must bind hierarchy completeness to its real VM/unit receipt.
// No path, PID, numerical limit or provider can select this constructor's scope.
func CaptureCurrent(ctx context.Context) (current *Current, failure error) {
	if ctx == nil {
		return nil, ErrUnknown
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state := &currentLinux{}
	defer func() {
		if failure != nil {
			failure = errors.Join(failure, state.close())
		}
	}()
	var err error
	state.proc, err = openAbsoluteDirectory(ctx, "/proc", unix.PROC_SUPER_MAGIC)
	if err != nil {
		return nil, errors.Join(ErrUnknown, err)
	}
	state.self, err = openChildDirectory(state.proc, "self", true)
	if err != nil {
		return nil, errors.Join(ErrUnknown, err)
	}
	state.member, state.mount, err = state.currentMapping(ctx)
	if err != nil {
		return nil, err
	}
	if err := state.openGroups(ctx); err != nil {
		return nil, err
	}
	if err := state.check(ctx); err != nil {
		return nil, err
	}
	return &Current{owner: ctx, backend: state}, nil
}

func (s *currentLinux) currentMapping(ctx context.Context) (string, memoryMount, error) {
	raw, err := readAccountingBytes(ctx, s.self, "cgroup")
	if err != nil {
		return "", memoryMount{}, errors.Join(ErrUnknown, err)
	}
	member, err := currentUnifiedPath(raw)
	if err != nil {
		return "", memoryMount{}, err
	}
	file, err := openAccountingFile(s.self, "mountinfo")
	if err != nil {
		return "", memoryMount{}, errors.Join(ErrUnknown, err)
	}
	mount, readErr := findMemoryMount(ctx, file.file, member)
	nativeErr := file.check()
	closeErr := file.file.Close()
	if err := errors.Join(readErr, nativeErr, closeErr); err != nil {
		return "", memoryMount{}, errors.Join(ErrUnknown, err)
	}
	return member, mount, nil
}

func (s *currentLinux) openGroups(ctx context.Context) error {
	root, err := openAbsoluteDirectory(ctx, s.mount.path, unix.CGROUP2_SUPER_MAGIC)
	if err != nil {
		return errors.Join(ErrUnknown, err)
	}
	s.groups = append(s.groups, root)
	if root.mountID != s.mount.id {
		return fmt.Errorf("native cgroup mount generation differs from mountinfo: %w", ErrUnknown)
	}
	relative, found := relativeGroup(s.mount.root, s.member)
	if !found {
		return ErrUnknown
	}
	if relative == "" {
		return nil
	}
	parent := root
	for _, component := range strings.Split(relative, "/") {
		if err := ctx.Err(); err != nil {
			return err
		}
		child, err := openChildDirectory(parent, component, false)
		if err != nil {
			return errors.Join(ErrUnknown, err)
		}
		s.groups = append(s.groups, child)
		parent = child
	}
	return nil
}

func (s *currentLinux) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.proc == nil || s.self == nil || len(s.groups) == 0 {
		return ErrUnknown
	}
	for _, directory := range append([]*nativeFile{s.proc, s.self}, s.groups...) {
		if err := directory.check(); err != nil {
			return errors.Join(ErrUnknown, err)
		}
	}
	member, mount, err := s.currentMapping(ctx)
	if err != nil {
		return err
	}
	if member != s.member || mount != s.mount {
		return fmt.Errorf("current native memory membership or mapping changed: %w", ErrUnknown)
	}
	return currentProcessInGroup(ctx, s.groups[len(s.groups)-1])
}

func currentProcessInGroup(ctx context.Context, group *nativeFile) (failure error) {
	file, err := openAccountingFile(group, "cgroup.procs")
	if err != nil {
		return errors.Join(ErrUnknown, err)
	}
	defer func() { failure = errors.Join(failure, file.file.Close()) }()
	scanner := bufio.NewScanner(contextReader{ctx: ctx, in: file.file})
	scanner.Split(bufio.ScanWords)
	found := false
	for scanner.Scan() {
		pid, err := decimal(scanner.Text())
		if err != nil || pid == 0 {
			return errors.Join(ErrUnknown, err)
		}
		found = found || pid == uint64(os.Getpid()) // Actual self, never a selected PID.
	}
	if err := errors.Join(scanner.Err(), file.check(), ctx.Err()); err != nil {
		return errors.Join(ErrUnknown, err)
	}
	if !found {
		return fmt.Errorf("native cgroup does not contain the current process: %w", ErrUnknown)
	}
	return nil
}

func (s *currentLinux) observe(ctx context.Context) (sampleData, error) {
	if err := s.check(ctx); err != nil {
		return sampleData{}, err
	}
	physical, err := s.physicalAvailable(ctx)
	if err != nil {
		return sampleData{}, err
	}
	ancestor, err := s.ancestorHeadroom(ctx)
	if err != nil {
		return sampleData{}, err
	}
	if err := s.check(ctx); err != nil {
		return sampleData{}, err
	}
	return sampleData{physical: physical, ancestor: ancestor}, nil
}

func (s *currentLinux) physicalAvailable(ctx context.Context) (available uint64, failure error) {
	file, err := openAccountingFile(s.proc, "meminfo")
	if err != nil {
		return 0, errors.Join(ErrUnknown, err)
	}
	defer func() { failure = errors.Join(failure, file.file.Close()) }()
	info, err := ReadMeminfo(ctx, file.file, MemTotal, MemAvailable)
	if err != nil {
		return 0, err
	}
	total, err := info.Value(MemTotal)
	if err != nil {
		return 0, err
	}
	available, err = info.Value(MemAvailable)
	if err != nil || total == 0 || available > total {
		return 0, errors.Join(ErrUnknown, err)
	}
	return available, errors.Join(file.check(), ctx.Err())
}

func (s *currentLinux) match(ctx context.Context, directory *os.File, rootOnly bool) error {
	if err := s.check(ctx); err != nil {
		return err
	}
	fresh, err := inspectNative(directory, unix.CGROUP2_SUPER_MAGIC, true)
	if err != nil {
		return errors.Join(ErrUnknown, err)
	}
	for i, expected := range s.groups {
		if rootOnly && i != 0 {
			break
		}
		if expected.mountID == fresh.mountID && os.SameFile(expected.info, fresh.info) && expected.info.Mode() == fresh.info.Mode() {
			return s.check(ctx)
		}
	}
	return fmt.Errorf("owned memory scope FD is not the captured native role: %w", ErrUnknown)
}

func (s *currentLinux) close() error {
	var errs []error
	for i := len(s.groups) - 1; i >= 0; i-- {
		errs = append(errs, s.groups[i].file.Close())
	}
	s.groups = nil
	for _, file := range []*nativeFile{s.self, s.proc} {
		if file != nil {
			errs = append(errs, file.file.Close())
		}
	}
	s.self, s.proc = nil, nil
	return errors.Join(errs...)
}
