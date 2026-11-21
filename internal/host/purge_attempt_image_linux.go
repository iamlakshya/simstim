//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/cachefs"
	"github.com/iamlakshya/simstim/internal/secretmem"
	"golang.org/x/sys/unix"
)

// This is a current-process code observation and inherited actual creator
// chain. It supplies no original OP, session, transaction or cleanup Source.
// Only the raw fixed entry can produce one, before reading request input.
type purgeAttemptImageV2 struct {
	mu                       sync.Mutex
	kernel, original, copied *os.File
	parent, cancelRequest    *os.File
	mapped, originalInfo     os.FileInfo
	custody                  *purgeCodeCustodyV3
	lifetime                 context.Context
	cancel                   context.CancelFunc
	watchers                 sync.WaitGroup
	closeDone                chan struct{}
	closeErr                 error
	closed                   bool
}

func adoptRawPurgeAttemptImageV2() (result *purgeAttemptImageV2, returned error) {
	// The creator must prove the supported unreadable static ELF/credential
	// class before REAL Start; 0100 alone is not a pre-Go protection proof.
	// Explicit protection is the first raw-boundary action here.
	protectionErr := secretmem.Protect()
	// Public lifetime custody only. The actual original starter validated and
	// froze this fifth inherited role before REAL Start. Do not let a parent
	// death, cancellation, image error or bootstrap EOF release this PID while
	// its manager adoption transaction may still contain the numeric PID.
	unix.CloseOnExec(purgeAttemptAdoptionFDV3)
	adoption := os.NewFile(uintptr(purgeAttemptAdoptionFDV3), "actual-original-purge-adoption-settlement")
	adoptionErr := waitPurgeAttemptAdoptionV3(adoption)
	adoptionCloseErr := adoption.Close()
	if err := errors.Join(protectionErr, adoptionErr, adoptionCloseErr); err != nil {
		return nil, err
	}
	state, err := secretmem.Inspect()
	if err != nil || state.Dumpable == nil || *state.Dumpable != 0 || state.CoreHard != 0 {
		return nil, errors.Join(placementError("the raw purge image has no current protected native process"), err)
	}
	if len(os.Args) != 2 || os.Args[1] != "purge-attempt-v2" {
		return nil, placementError("the raw purge image has another closed command")
	}
	lifetime, cancel := context.WithCancel(context.Background())
	image := &purgeAttemptImageV2{lifetime: lifetime, cancel: cancel, closeDone: make(chan struct{})}
	complete := false
	defer func() {
		if !complete {
			closeErr := image.Close()
			returned = errors.Join(returned, closeErr)
			if closeErr != nil {
				result = image
			}
		}
	}()
	fd, err := unix.Open("/proc/self/exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	image.kernel = os.NewFile(uintptr(fd), "actual-kernel-mapped-purge-image")
	image.mapped, err = image.kernel.Stat()
	if err != nil {
		return nil, err
	}
	_, _, uid, links, known := footprintIdentity(image.mapped)
	if !known || uid != uint64(os.Getuid()) || links != 1 || !image.mapped.Mode().IsRegular() || image.mapped.Mode().Perm() != 0o100 {
		return nil, placementError("the raw purge entry is not its actual single-link execute-only mapped image")
	}
	for fd, target := range map[int]**os.File{3: &image.original, 4: &image.copied, 5: &image.parent, 6: &image.cancelRequest} {
		unix.CloseOnExec(fd)
		*target = os.NewFile(uintptr(fd), "actual-original-purge-bootstrap-role")
	}
	info, err := cachefs.ValidateDescriptor(image.copied, 0o100)
	if err != nil || !os.SameFile(info, image.mapped) {
		return nil, errors.Join(placementError("the inherited readable code differs from this actual kernel mapping"), err)
	}
	if err := checkPurgeKernelExecEligibilityV2(context.Background(), image.copied); err != nil {
		return nil, err
	}
	image.originalInfo, err = image.original.Stat()
	if err != nil {
		return nil, err
	}
	if _, err := cachefs.ValidateDescriptor(image.original, image.originalInfo.Mode().Perm()); err != nil {
		return nil, err
	}
	if err := verifyOriginalPurgeCodeBytesV2(context.Background(), image.original, image.copied, image.originalInfo); err != nil {
		return nil, err
	}
	if err := unix.PidfdSendSignal(int(image.parent.Fd()), 0, nil, 0); err != nil {
		return nil, err
	}
	if ordinaryJoinLatchV1(image.cancelRequest, false) != nil && ordinaryJoinLatchV1(image.cancelRequest, true) != nil {
		return nil, placementError("the raw purge request has no actual original native cancellation object")
	}
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	complete = true
	return image, nil
}

func (image *purgeAttemptImageV2) checkLockedV2(ctx context.Context) error {
	if image == nil || ctx == nil || image.closed || image.kernel == nil || image.original == nil || image.copied == nil || image.mapped == nil {
		return placementError("the current purge code image ended")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Open("/proc/self/exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	fresh := os.NewFile(uintptr(fd), "current-kernel-purge-code-mapping")
	info, err := fresh.Stat()
	closed := fresh.Close()
	if err != nil || closed != nil || !os.SameFile(info, image.mapped) {
		return errors.Join(placementError("the purge actor changed its actual kernel-mapped image"), err, closed)
	}
	copyInfo, err := cachefs.ValidateDescriptor(image.copied, 0o100)
	if err != nil && image.custody != nil && image.custody.image == image && image.custody.codeInfo != nil {
		// Only the same actual factory custody may retain its native mapping
		// after its own terminal unlink. Missing names alone never qualify.
		actual, statErr := image.copied.Stat()
		_, _, uid, links, known := footprintIdentity(actual)
		if statErr == nil && known && links == 0 && uid == uint64(os.Getuid()) && actual.Mode().IsRegular() && actual.Mode().Perm() == 0o100 && os.SameFile(actual, image.custody.codeInfo) && os.SameFile(actual, image.mapped) {
			copyInfo, err = actual, nil
		}
	}
	if err != nil || !os.SameFile(copyInfo, image.mapped) {
		return errors.Join(placementError("the retained purge image code descriptor changed"), err)
	}
	return verifyOriginalPurgeCodeBytesV2(ctx, image.original, image.copied, image.originalInfo)
}

// Check binds this current kernel mapping to the retained original code
// descriptors. It cannot accept another caller's expected-file data.
func (image *purgeAttemptImageV2) Check(ctx context.Context) error {
	if image == nil {
		return placementError("the current purge image is absent")
	}
	image.mu.Lock()
	defer image.mu.Unlock()
	return image.checkLockedV2(ctx)
}

// This checks ONLY the inherited live parent handle and public code-file
// observation. It does NOT prove that parent's current mm code and MUST NOT
// authorise private admission. A genuine execution/controller issuer is still
// required for that separate native hook. Kernel fdinfo remains bracketed by
// the original pidfd; saved PIDs and public frames cannot replace its object.
func (image *purgeAttemptImageV2) checkOriginalParentHandleV2(peer *nativeStartupPeerV1, sender startupCredentials) error {
	if image == nil {
		return placementError("the current purge image is absent")
	}
	image.mu.Lock()
	defer image.mu.Unlock()
	if err := image.checkLockedV2(context.Background()); err != nil {
		return err
	}
	if peer == nil || peer.identity == nil || image.parent == nil {
		return placementError("the raw purge bootstrap has no original native parent")
	}
	if err := errors.Join(peer.CheckSender(sender), unix.PidfdSendSignal(int(image.parent.Fd()), 0, nil, 0)); err != nil {
		return err
	}
	raw, err := ordinaryFDInfoV1(image.parent)
	if err != nil {
		return err
	}
	pid, found := 0, false
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "Pid:" {
			if found {
				return placementError("the original parent descriptor has duplicate native identity")
			}
			pid, err = strconv.Atoi(fields[1])
			if err != nil || pid <= 0 {
				return placementError("the original native parent descriptor has no live process identity")
			}
			found = true
		}
	}
	if !found || peer.pid != pid || sender.pid != pid || os.Getppid() != pid {
		return placementError("the current bootstrap sender differs from the original native fork parent")
	}
	actual, err := peer.identity.executable.Stat()
	if err != nil || !os.SameFile(actual, image.originalInfo) {
		return errors.Join(placementError("the original native parent supplied a different public code object"), err)
	}
	return errors.Join(peer.CheckSender(sender), unix.PidfdSendSignal(int(image.parent.Fd()), 0, nil, 0))
}

// watchOriginalRequestV2 cancels one real request when its actual parent
// exits or signals its existing cancellation event. It never ends the owned
// transaction lifetime or supplies release permission after an actual stop.
func (image *purgeAttemptImageV2) watchOriginalRequestV2(parent context.Context) (context.Context, func(), error) {
	if image == nil {
		return nil, nil, placementError("the current purge image is absent")
	}
	image.mu.Lock()
	if err := image.checkLockedV2(parent); err != nil {
		image.mu.Unlock()
		return nil, nil, err
	}
	fd, err := unix.FcntlInt(image.parent.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		image.mu.Unlock()
		return nil, nil, err
	}
	cancelFD, err := unix.FcntlInt(image.cancelRequest.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		image.mu.Unlock()
		return nil, nil, errors.Join(err, unix.Close(fd))
	}
	image.watchers.Add(1)
	image.mu.Unlock()
	ctx, cancel := context.WithCancel(parent)
	stopLifetime := context.AfterFunc(image.lifetime, cancel)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer image.watchers.Done()
		defer unix.Close(fd)
		defer unix.Close(cancelFD)
		for ctx.Err() == nil {
			polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}, {Fd: int32(cancelFD), Events: unix.POLLIN}}
			n, err := unix.Poll(polls, 100)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || n != 0 {
				cancel()
				return
			}
		}
	}()
	return ctx, func() { stopLifetime(); cancel(); <-done }, nil
}

// Close cancels and joins this image's actual request observers before its
// borrowed code and native parent descriptors close.
func (image *purgeAttemptImageV2) Close() error {
	if image == nil {
		return nil
	}
	image.mu.Lock()
	if image.closed {
		done := image.closeDone
		image.mu.Unlock()
		if done != nil {
			<-done
		}
		return image.closeErr
	}
	image.closed = true
	if image.cancel != nil {
		image.cancel()
	}
	image.mu.Unlock()
	image.watchers.Wait()
	for _, file := range []*os.File{image.kernel, image.original, image.copied, image.parent, image.cancelRequest} {
		if file != nil {
			image.closeErr = errors.Join(image.closeErr, file.Close())
		}
	}
	if image.closeDone != nil {
		close(image.closeDone)
	}
	return image.closeErr
}
