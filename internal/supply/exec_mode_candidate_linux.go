//go:build linux

package supply

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

type execModeCandidate struct {
	artifacts, modes         *os.Root
	store                    *os.Root
	artifactRef, modeRootRef cachefs.Reference
	storeRef, lockRef        cachefs.Reference
	domain                   cachefs.Domain
	item                     Item
	program                  *os.File // borrowed from VerifiedRuntime
	lease                    *ExecModeLeaseV1
	closed                   bool
	closing                  bool
}

// PrepareRuntimeWithModeV1 consumes actual anchored store capabilities, not a
// pathname or public DTO. The runtimecache owner supplies its checked original
// domain/transaction. This verifies DATA only; actual Need/OP, native Cmd and
// private projection/final-exec issuers remain the boot authority.
// Native read-only artifact mounts retain the strict sealed VMM route. That
// compatibility boundary remains an explicit open specification decision.
func PrepareRuntimeWithModeV1(ctx context.Context, item Item, source *os.File, artifacts, modes, store *os.Root, domain cachefs.Domain) (*VerifiedRuntime, error) {
	if item.Kind != KindVMM {
		return PrepareRuntime(ctx, item, source)
	}
	if err := validateSealedRuntimeItem(item); err != nil {
		return nil, err
	}
	if _, err := runtimeStoreDescriptor(item, source, false); err != nil {
		return nil, err
	}
	got, err := measureFSVerity(source)
	if err != nil {
		if verityAbsent(err) {
			return PrepareRuntime(ctx, item, source)
		}
		return nil, err
	}
	if err := compareVerity(item, got); err != nil {
		return nil, err
	}
	if err := CheckOpenContext(ctx, item, source); err != nil {
		return nil, err
	}
	writable, err := modeMountWritable(source)
	if err != nil {
		return nil, err
	}
	if !writable {
		return PrepareRuntime(ctx, item, source)
	}
	if artifacts == nil || modes == nil || store == nil {
		return nil, fmt.Errorf("writable direct runtime lacks its original guarded store roots")
	}
	artifactsCopy, err := artifacts.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	modesCopy, err := modes.OpenRoot(".")
	if err != nil {
		artifactsCopy.Close()
		return nil, err
	}
	storeCopy, err := store.OpenRoot(".")
	if err != nil {
		artifactsCopy.Close()
		modesCopy.Close()
		return nil, err
	}
	returned := false
	defer func() {
		if !returned {
			storeCopy.Close()
		}
	}()
	a, err := cachefs.CaptureRoot(artifactsCopy)
	if err != nil {
		artifactsCopy.Close()
		modesCopy.Close()
		return nil, err
	}
	m, err := cachefs.CaptureRoot(modesCopy)
	if err != nil {
		artifactsCopy.Close()
		modesCopy.Close()
		return nil, err
	}
	s, err := cachefs.CaptureRoot(storeCopy)
	if err != nil {
		artifactsCopy.Close()
		modesCopy.Close()
		return nil, err
	}
	lock, err := cachefs.OpenFile(storeCopy, ".lock", os.O_RDONLY, 0o600)
	if err != nil {
		artifactsCopy.Close()
		modesCopy.Close()
		return nil, err
	}
	lockRef, err := cachefs.CaptureFile(storeCopy, ".lock", lock, 0o600)
	lock.Close()
	if err != nil {
		artifactsCopy.Close()
		modesCopy.Close()
		return nil, err
	}
	file, err := openModeReadable(artifactsCopy, item, source)
	if err != nil {
		artifactsCopy.Close()
		modesCopy.Close()
		return nil, err
	}
	candidate := &execModeCandidate{artifacts: artifactsCopy, modes: modesCopy, store: storeCopy, artifactRef: a, modeRootRef: m, storeRef: s, lockRef: lockRef, domain: domain, item: item, program: file}
	returned = true
	return &VerifiedRuntime{file: file, item: item, mode: candidate}, nil
}

func (candidate *execModeCandidate) checkProgram(ctx context.Context) error {
	if candidate == nil || candidate.closed || candidate.closing || candidate.program == nil {
		return fmt.Errorf("direct runtime candidate is closed")
	}
	if err := candidate.artifactRef.CheckRoot(candidate.artifacts); err != nil {
		return err
	}
	if err := candidate.modeRootRef.CheckRoot(candidate.modes); err != nil {
		return err
	}
	info, err := modeArtifactInfo(candidate.artifacts, candidate.item, candidate.program)
	if err != nil {
		return err
	}
	if info.Mode() != 0o555 && info.Mode() != 0o111 {
		return fmt.Errorf("direct runtime candidate lost its native mode")
	}
	got, err := measureFSVerity(candidate.program)
	if err != nil {
		return err
	}
	if err := compareVerity(candidate.item, got); err != nil {
		return err
	}
	return ctx.Err()
}

// BeginExecMode is lazy: the actual driver invokes it after all Reference
// writes and before constructing ANY projection. Nil means this owning
// VerifiedRuntime actually selected the sealed route, never a caller flag.
func (v *VerifiedRuntime) BeginExecMode(ctx context.Context) (*ExecModeLeaseV1, error) {
	if v == nil {
		return nil, fmt.Errorf("verified runtime owner is missing")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.file == nil {
		return nil, os.ErrClosed
	}
	if v.mode == nil {
		return nil, nil
	}
	return v.mode.begin(ctx)
}

func (candidate *execModeCandidate) begin(ctx context.Context) (*ExecModeLeaseV1, error) {
	if err := candidate.checkProgram(ctx); err != nil {
		return nil, err
	}
	if candidate.lease != nil {
		return nil, fmt.Errorf("direct runtime mode phase is already owned")
	}
	for {
		lease, err := candidate.tryBegin(ctx)
		if !errors.Is(err, ErrRuntimeModeBusy) {
			if err == nil {
				candidate.lease = lease
			}
			return lease, err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (candidate *execModeCandidate) tryBegin(ctx context.Context) (*ExecModeLeaseV1, error) {
	if err := candidate.storeRef.CheckRoot(candidate.store); err != nil {
		return nil, err
	}
	lock, err := cachefs.OpenFile(candidate.store, ".lock", os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := candidate.lockRef.CheckCreatedFile(candidate.store, ".lock", lock, 0o600); err != nil {
		return nil, err
	}
	held, err := cachefs.TryLock(lock, cachefs.Exclusive)
	if err != nil {
		return nil, err
	}
	if !held {
		return nil, ErrRuntimeModeBusy
	}
	actual, found, err := cachefs.ReadDomainBinding(ctx, candidate.store, ".lock", lock)
	if err != nil {
		return nil, err
	}
	if candidate.domain.IsZero() {
		if found {
			return nil, fmt.Errorf("fixture mode source acquired a host domain")
		}
	} else if !found || actual != candidate.domain {
		return nil, fmt.Errorf("runtime mode source lost its original host domain")
	}
	guard, err := createRuntimeModeGuard(ctx, candidate.artifacts, candidate.modes, candidate.item)
	if err != nil {
		return nil, err
	}
	lease, err := newExecModeLeaseV1(ctx, guard, candidate.program)
	if err != nil {
		guard.Close()
		return nil, err
	}
	// Only the ORIGINAL per-artifact OFD remains locked. No store lock is
	// retained while projections, helpers, callbacks or final exec run.
	return lease, nil
}

func (candidate *execModeCandidate) close() error {
	if candidate == nil || candidate.closed {
		return nil
	}
	candidate.closing = true
	if candidate.lease != nil {
		if err := candidate.lease.Close(); err != nil {
			return err
		}
	}
	for _, root := range []**os.Root{&candidate.artifacts, &candidate.modes, &candidate.store} {
		if *root == nil {
			continue
		}
		if err := (*root).Close(); err != nil {
			return err
		}
		*root = nil
	}
	candidate.closed = true
	return nil
}
