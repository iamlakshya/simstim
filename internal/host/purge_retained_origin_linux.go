//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"golang.org/x/sys/unix"
)

// This factory retains the actual purge origin before any subject capture.
// It supplies only original-origin checks after authorised namespace unlink.
// A cleanup phase, missing path or saved root reference cannot create it.
type purgeRetainedOriginV2 struct {
	mu       sync.Mutex
	owner    *purgeOwnerV1
	code     *os.File
	codeInfo os.FileInfo
	roots    []*purgeRetainedRootV2
	files    []*purgeRetainedFileV2
	ordinary map[*ordinaryHolderCaptureV2]*SessionOperation
	closed   bool
	closeErr error
}

type purgeRetainedRootV2 struct {
	root *os.Root
	path string
	ref  purgeRef
}

type purgeRetainedFileV2 struct {
	root *os.Root
	leaf string
	file *os.File
	info os.FileInfo
}

type purgeRetainedOriginKeyV2 struct{}

func newPurgeRetainedOriginV2(ctx context.Context, owner *purgeOwnerV1) (result *purgeRetainedOriginV2, returned error) {
	if ctx == nil || owner == nil || owner.placement == nil || len(owner.roots) == 0 {
		return nil, placementError("the retained origin has no real purge owner and already-pinned roots")
	}
	if err := errors.Join(ctx.Err(), CheckCaller(), owner.placement.checkIdentity()); err != nil {
		return nil, err
	}
	origin := &purgeRetainedOriginV2{owner: owner, ordinary: map[*ordinaryHolderCaptureV2]*SessionOperation{}}
	complete := false
	defer func() {
		if !complete {
			closeErr := origin.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = origin
			}
		}
	}()
	fd, err := unix.Open("/proc/self/exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	origin.code = os.NewFile(uintptr(fd), "actual-purge-origin-mapped-code")
	origin.codeInfo, err = origin.code.Stat()
	if err != nil || !os.SameFile(origin.codeInfo, owner.placement.executableRef) {
		return nil, errors.Join(placementError("the retained origin has another actual mapped image"), err)
	}
	if err := owner.gate(func(control *placementControl, guard *os.Root) error {
		root, err := guard.OpenRoot(".")
		if err != nil {
			return err
		}
		origin.roots = append(origin.roots, &purgeRetainedRootV2{root: root, path: owner.placement.control, ref: control.Control})
		if err := origin.retainFileV2(root, "control.lock", owner.placement.controlLockRef); err != nil {
			return err
		}
		for _, role := range []string{"state", "data", "config"} {
			pin := owner.roots[role]
			if pin == nil {
				continue
			}
			path, err := purgeRootPath(control, role)
			if err != nil {
				return err
			}
			if _, err := owner.checkRoot(role, control); err != nil {
				return err
			}
			copy, err := pin.root.OpenRoot(".")
			if err != nil {
				return err
			}
			origin.roots = append(origin.roots, &purgeRetainedRootV2{root: copy, path: path, ref: pin.ref})
			if role == "state" {
				if err := origin.retainFileV2(copy, "host-id", owner.placement.hostRef); err != nil {
					return err
				}
				if err := origin.retainFileV2(copy, "place.lock", owner.placement.placeRef); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := origin.checkNativeLockedV2(ctx); err != nil {
		return nil, err
	}
	complete = true
	return origin, nil
}

func (origin *purgeRetainedOriginV2) retainFileV2(root *os.Root, leaf string, expected os.FileInfo) error {
	file, err := cachefs.OpenFile(root, leaf, os.O_RDONLY, 0o600)
	if err != nil {
		return err
	}
	info, err := cachefs.ValidateFile(root, leaf, file, 0o600)
	if err != nil || expected == nil || !os.SameFile(info, expected) {
		return errors.Join(placementError("the actual purge origin sentinel changed"), err, file.Close())
	}
	origin.files = append(origin.files, &purgeRetainedFileV2{root: root, leaf: leaf, file: file, info: info})
	return nil
}

func (origin *purgeRetainedOriginV2) checkNativeLockedV2(ctx context.Context) error {
	if origin == nil || ctx == nil || origin.closed || origin.owner == nil || origin.owner.placement == nil || origin.code == nil || origin.codeInfo == nil || len(origin.roots) == 0 {
		return placementError("the actual retained purge origin ended")
	}
	if err := errors.Join(ctx.Err(), CheckCaller()); err != nil {
		return err
	}
	fd, err := unix.Open("/proc/self/exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	fresh := os.NewFile(uintptr(fd), "current-purge-origin-kernel-image")
	info, err := fresh.Stat()
	closed := fresh.Close()
	retained, retainedErr := origin.code.Stat()
	if err != nil || closed != nil || retainedErr != nil || !os.SameFile(info, origin.codeInfo) || !os.SameFile(retained, origin.codeInfo) {
		return errors.Join(placementError("the actual purge origin changed its retained kernel image"), err, closed, retainedErr)
	}
	for _, root := range origin.roots {
		if err := root.checkV2(); err != nil {
			return err
		}
	}
	for _, file := range origin.files {
		if err := file.checkV2(); err != nil {
			return err
		}
	}
	return nil
}

func (origin *purgeRetainedOriginV2) contextV2(ctx context.Context) (context.Context, error) {
	if origin == nil {
		return ctx, placementError("the retained purge origin is absent")
	}
	origin.mu.Lock()
	defer origin.mu.Unlock()
	if err := origin.checkNativeLockedV2(ctx); err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, purgeRetainedOriginKeyV2{}, origin), nil
}

// Registration happens only after the actual capture producer has checked
// its original holder OFD, peer/code, creation handles and SAME original OP.
func bindPurgeOrdinaryOriginV2(ctx context.Context, captured *ordinaryHolderCaptureV2) error {
	origin, _ := ctx.Value(purgeRetainedOriginKeyV2{}).(*purgeRetainedOriginV2)
	if origin == nil {
		return nil
	}
	origin.mu.Lock()
	defer origin.mu.Unlock()
	if origin.owner == nil || origin.owner.placement == nil || captured == nil || captured.operation == nil || captured.origin != origin.owner.placement || captured.stopSent || captured.settled || captured.closed || !captured.operation.Valid(captured.operation.Primary()) {
		return placementError("retained origin registration has no original live ordinary capture")
	}
	if err := errors.Join(origin.checkNativeLockedV2(ctx), captured.checkCurrentOrdinaryV1(ctx), captured.holder.check(ctx)); err != nil {
		return err
	}
	if old, found := origin.ordinary[captured]; found && old != captured.operation {
		return placementError("the actual ordinary capture changed its original operation")
	}
	origin.ordinary[captured] = captured.operation
	captured.retainedOrigin = origin
	return nil
}

func checkOrdinaryPurgeOriginV2(ctx context.Context, captured *ordinaryHolderCaptureV2) error {
	if captured == nil || captured.origin == nil {
		return placementError("the ordinary native origin is absent")
	}
	origin := captured.retainedOrigin
	if origin == nil {
		return captured.origin.checkIdentity()
	}
	origin.mu.Lock()
	defer origin.mu.Unlock()
	if origin.owner == nil || origin.owner.placement != captured.origin || origin.ordinary[captured] != captured.operation || captured.operation == nil || captured.operation.native == nil || captured.operation.native.borrowed || !captured.operation.active.Load() {
		return placementError("the stopped ordinary source lost its same retained origin registration")
	}
	return origin.checkNativeLockedV2(ctx)
}

// Close joins only descriptors created by this read-only origin factory.
// Actual captured subjects and their OPs must close first after settlement.
func (origin *purgeRetainedOriginV2) Close() error {
	if origin == nil {
		return nil
	}
	origin.mu.Lock()
	defer origin.mu.Unlock()
	if origin.closed {
		return origin.closeErr
	}
	for captured, operation := range origin.ordinary {
		if captured == nil || operation == nil || !captured.closed || operation.active.Load() || operation.native == nil {
			return placementError("the retained origin still has an actual ordinary subject or operation")
		}
		for _, file := range []*os.File{captured.main, captured.joined, captured.holder.file, operation.native.file} {
			if file == nil {
				return placementError("the original subject closure lost a native descriptor")
			}
			if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
				return errors.Join(placementError("the retained ordinary native description has not actually closed"), err)
			}
		}
	}
	for _, file := range origin.files {
		origin.closeErr = errors.Join(origin.closeErr, file.file.Close())
	}
	for _, root := range origin.roots {
		origin.closeErr = errors.Join(origin.closeErr, root.root.Close())
	}
	if origin.code != nil {
		origin.closeErr = errors.Join(origin.closeErr, origin.code.Close())
	}
	origin.closed = true
	return origin.closeErr
}

func (root *purgeRetainedRootV2) checkV2() error {
	if root == nil || root.root == nil || !filepath.IsAbs(root.path) {
		return placementError("the actual retained root is absent")
	}
	file, err := root.root.Open(".")
	if err != nil {
		return err
	}
	info, err := file.Stat()
	closed := file.Close()
	if err != nil || closed != nil || !root.ref.matches(info) {
		return errors.Join(placementError("the original retained root changed its native identity"), err, closed)
	}
	_, _, _, links, known := footprintIdentity(info)
	if !known {
		return placementError("the original retained root has unknown native link state")
	}
	fresh, err := cachefs.OpenPrivateRoot(root.path, false)
	if errors.Is(err, os.ErrNotExist) && links == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.Join(root.ref.matchesRoot(fresh), fresh.Close())
}

func (file *purgeRetainedFileV2) checkV2() error {
	if file == nil || file.root == nil || file.file == nil || file.info == nil {
		return placementError("the original retained sentinel is absent")
	}
	info, err := file.file.Stat()
	if err != nil || !os.SameFile(info, file.info) || info.Mode() != file.info.Mode() || info.Size() != file.info.Size() || !info.ModTime().Equal(file.info.ModTime()) {
		return errors.Join(placementError("the actual retained sentinel changed"), err)
	}
	_, _, _, links, known := footprintIdentity(info)
	if !known {
		return placementError("the retained sentinel has unknown native link state")
	}
	_, err = file.root.Lstat(file.leaf)
	if errors.Is(err, os.ErrNotExist) && links == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = cachefs.ValidateFile(file.root, file.leaf, file.file, 0o600)
	return err
}
