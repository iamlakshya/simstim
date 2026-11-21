//go:build linux

package gvisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"sync"

	"github.com/iamlakshya/simstim/internal/supply"
	"golang.org/x/sys/unix"
)

// ProtectedSuiteV1 retains the SAME signed sealed runtime objects opened by
// this actual Runtime. It is public code custody, not namespace, readiness or
// private-input permission. Runtime.mu protects its publication and lifetime.
// No exported constructor can manufacture one from descriptors or a manifest.
type ProtectedSuiteV1 struct {
	original *ProtectedSuiteV1
	runtime  *Runtime
	items    []supply.Item
	files    []*os.File
	closed   bool
	closeErr error
}

// ControllerCodeV1 is an owned public code projection, never a launch receipt.
// Its caller must separately retain the original boot, C creation, topology,
// native witness and final aggregate owners before private input is possible.
type ControllerCodeV1 struct {
	parentOwned                                       bool
	mu                                                sync.Mutex
	closeErr                                          error
	Runsc, ProcUmounter, Sentry, FDParking, Prewarmer *os.File
}

// Close releases only this projection's retained public code descriptions.
func (code *ControllerCodeV1) Close() error {
	if code == nil {
		return nil
	}
	code.mu.Lock()
	defer code.mu.Unlock()
	return code.closeLockedV1()
}
func (code *ControllerCodeV1) closeLockedV1() error {
	if code.closeErr != nil {
		return code.closeErr
	}
	for _, field := range []**os.File{&code.Runsc, &code.ProcUmounter, &code.Sentry, &code.FDParking, &code.Prewarmer} {
		if *field == nil {
			continue
		}
		if err := (*field).Close(); err != nil {
			code.parentOwned = true
			code.closeErr = errors.Join(code.closeErr, err)
		} else {
			*field = nil
		}
	}
	return code.closeErr
}

func (code *ControllerCodeV1) closeParentOwnedV1() error {
	code.mu.Lock()
	defer code.mu.Unlock()
	if !code.parentOwned {
		return nil
	}
	return code.closeLockedV1()
}

// A duplicate preserves O_RDWR. Derive a read-only open description through
// this process's own held descriptor, then require the SAME sealed inode and
// signed bytes. A path replacement, failed open or EACCES creates no source.
func readonlyGvisorCodeV1(item supply.Item, source *os.File) (result *os.File, failureErr error) {
	if source == nil {
		return nil, fmt.Errorf("the protected gvisor suite lost an original executable")
	}
	before, err := source.Stat()
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open("/proc/self/fd/"+strconv.Itoa(int(source.Fd())), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	held := os.NewFile(uintptr(fd), "same-sealed-readonly-gvisor-code")
	complete := false
	defer func() {
		if !complete {
			closeErr := held.Close()
			failureErr = errors.Join(failureErr, closeErr)
			if closeErr != nil {
				result = held
			}
		}
	}()
	after, err := held.Stat()
	if err != nil || !os.SameFile(before, after) {
		return nil, errors.Join(err, fmt.Errorf("read-only gvisor code differs from its original held inode"))
	}
	if err := supply.CheckSealedRuntime(item, held); err != nil {
		return nil, err
	}
	flags, err := unix.FcntlInt(held.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY || flags&unix.O_PATH != 0 {
		return nil, errors.Join(err, fmt.Errorf("the protected gvisor code is not readable immutable code"))
	}
	complete = true
	return held, nil
}

// Called only by openRuntime after the complete original signed item loop.
// Failed construction closes its own derived handles; original Runtime files
// remain owned by the original caller's existing failure/physical-join path.
func captureProtectedSuiteV1(ctx context.Context, owner *Runtime, items []supply.Item) (result *ProtectedSuiteV1, failureErr error) {
	if owner == nil || owner.protectedSuite != nil || len(items) != len(owner.executables) {
		return nil, fmt.Errorf("gvisor has no complete original runtime suite")
	}
	suite := &ProtectedSuiteV1{runtime: owner, items: slices.Clone(items)}
	suite.original = suite
	complete := false
	defer func() {
		if !complete {
			result = suite
			failureErr = errors.Join(failureErr, suite.closeLockedV1())
		}
	}()
	for index, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if item.Kind != supply.KindGvisor || item.Arch != runtime.GOARCH || owner.executables[index].Target != RuntimeTargetV1(item.Name) {
			return nil, fmt.Errorf("the protected gvisor suite changed its signed executable role")
		}
		file, err := readonlyGvisorCodeV1(item, owner.executables[index].File)
		if file != nil {
			suite.files = append(suite.files, file)
		}
		if err != nil {
			return nil, err
		}
	}
	complete = true
	return suite, nil
}

func (suite *ProtectedSuiteV1) checkLockedV1(ctx context.Context, manifest supply.Manifest, executables []Executable) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if suite.original != suite || suite.closed || suite.closeErr != nil || suite.runtime == nil || suite.runtime.closing || suite.runtime.closed || suite.runtime.protectedSuite != suite || len(suite.files) != len(suite.items) || len(suite.files) != len(suite.runtime.executables) {
		return fmt.Errorf("the original protected gvisor suite ended or changed")
	}
	items, err := RuntimeItemsV1(manifest, runtime.GOARCH)
	if err != nil {
		return err
	}
	if !slices.Equal(items, suite.items) {
		return fmt.Errorf("the protected gvisor suite differs from this original signed runtime")
	}
	if executables == nil {
		executables = suite.runtime.executables
	}
	if len(executables) != len(items) {
		return fmt.Errorf("the protected gvisor suite lost an executable projection")
	}
	for index, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if executables[index].Target != RuntimeTargetV1(item.Name) || executables[index].File == nil {
			return fmt.Errorf("the protected gvisor projection changed a signed role")
		}
		actual, err := executables[index].File.Stat()
		expected, expectedErr := suite.files[index].Stat()
		original, originalErr := suite.runtime.executables[index].File.Stat()
		if err != nil || expectedErr != nil || originalErr != nil || !os.SameFile(actual, expected) || !os.SameFile(original, expected) {
			return errors.Join(err, expectedErr, originalErr, fmt.Errorf("the gvisor projection replaced an original signed native object"))
		}
		if err := supply.CheckSealedRuntime(item, suite.files[index]); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// CheckOriginalV1 rechecks the original runtime, signed roles and native objects.
func (suite *ProtectedSuiteV1) CheckOriginalV1(ctx context.Context, manifest supply.Manifest, executables []Executable) error {
	if suite == nil || suite.runtime == nil || ctx == nil {
		return fmt.Errorf("the original protected gvisor suite is absent")
	}
	suite.runtime.mu.Lock()
	defer suite.runtime.mu.Unlock()
	return suite.checkLockedV1(ctx, manifest, executables)
}

// RetainControllerCodeV1 retains all five distinct fixed companions. The supported
// runtime selector requires every role from the same exact patched build.
func (suite *ProtectedSuiteV1) RetainControllerCodeV1(ctx context.Context, manifest supply.Manifest) (_ *ControllerCodeV1, failureErr error) {
	if suite == nil || suite.runtime == nil || ctx == nil {
		return nil, fmt.Errorf("the original protected controller code suite is absent")
	}
	suite.runtime.mu.Lock()
	defer suite.runtime.mu.Unlock()
	if err := suite.checkLockedV1(ctx, manifest, nil); err != nil {
		return nil, err
	}
	code := &ControllerCodeV1{}
	suite.runtime.projections = append(suite.runtime.projections, code)
	roles := []struct {
		name   string
		target **os.File
	}{{"runsc", &code.Runsc}, {"gvisor-proc-umounter", &code.ProcUmounter}, {"gvisor_sentry", &code.Sentry}, {"runsc-fd-parking", &code.FDParking}, {"gvisor-sentry-prewarmer", &code.Prewarmer}}
	complete := false
	defer func() {
		if !complete {
			code.mu.Lock()
			code.parentOwned = true
			code.mu.Unlock()
			failureErr = errors.Join(failureErr, code.Close())
		}
	}()
	seen := map[[2]uint64]bool{}
	for _, role := range roles {
		found := -1
		for index, item := range suite.items {
			if item.Name == role.name {
				found = index
				break
			}
		}
		if found < 0 {
			return nil, fmt.Errorf("the original controller suite lacks fixed companion %s", role.name)
		}
		var st unix.Stat_t
		if err := unix.Fstat(int(suite.files[found].Fd()), &st); err != nil {
			return nil, err
		}
		key := [2]uint64{uint64(st.Dev), st.Ino}
		if seen[key] {
			return nil, fmt.Errorf("the protected controller code aliases distinct signed roles")
		}
		seen[key] = true
		fd, err := unix.FcntlInt(suite.files[found].Fd(), unix.F_DUPFD_CLOEXEC, 32)
		if err != nil {
			return nil, err
		}
		*role.target = os.NewFile(uintptr(fd), "same-original-fixed-controller-code")
	}
	complete = true
	return code, nil
}

func (suite *ProtectedSuiteV1) closeLockedV1() error {
	if suite == nil {
		return nil
	}
	if suite.closeErr != nil {
		return suite.closeErr
	}
	if suite.closed {
		return nil
	}
	suite.closed = true
	for index, file := range suite.files {
		if file == nil {
			continue
		}
		if err := file.Close(); err != nil {
			suite.closeErr = errors.Join(suite.closeErr, err)
		} else {
			suite.files[index] = nil
		}
	}
	if suite.closeErr != nil {
		return suite.closeErr
	}
	suite.files = nil
	return nil
}
