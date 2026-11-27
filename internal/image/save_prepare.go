package image

import (
	"context"
	"errors"
	"io"
	"os"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

// SavedRootConverter invokes the canonical jailed converter. The producer
// writes only after the actual owned jail starts, and returns after joining.
type SavedRootConverter func(context.Context, SavedRootPlan, func(context.Context, io.Writer) error, *os.File) (SavedRootReport, error)

// PreparedSave owns one native incoming root through review. Its token is
// correlation only; the original connection retains this exact object.
type PreparedSave struct {
	mu                    sync.Mutex
	store                 *Store
	owner                 context.Context
	id                    string
	stage                 string
	root                  *os.Root
	directory             cachefs.Reference
	output                *os.File
	creator               createdImageFile
	metadata              *createdImageFile
	pendingLease          *Lease
	pendingHead           imageFileWitness
	record                Record
	review                SaveReview
	replace               bool
	deadline              time.Time
	timer                 *time.Timer
	timerDone             chan struct{}
	timerStop             sync.Once
	done                  chan struct{}
	closed, durable, kept bool
	closeErr              error
}

// PrepareSave converts under the original host store's admission, then leaves
// the unpublished root in incoming. It writes no plan, values or unscanned tar.
func (store *Store) PrepareSave(ctx, owner context.Context, request SaveRequest, plan SavedRootPlan, produce func(context.Context, io.Writer) error, convert SavedRootConverter) (prepared *PreparedSave, returned error) {
	if err := store.requireWritableHost(); err != nil {
		return nil, err
	}
	if ctx == nil || owner == nil || owner.Err() != nil || store.domain.IsZero() || store.domain.Validate() != nil || !ValidSaveRef(request.Ref) || produce == nil || convert == nil {
		return nil, saveProtocol("image save has no original host store, reference or owned producer")
	}
	if _, err := newSavedRootFilter(plan); err != nil {
		return nil, err
	}
	token, err := randomName()
	if err != nil {
		return nil, err
	}
	prepared = &PreparedSave{store: store, owner: owner, id: token, stage: "incoming/.image-stage-" + token, replace: request.Replace, done: make(chan struct{}), timerDone: make(chan struct{})}
	owned := prepared
	complete := false
	defer func() {
		if !complete {
			returned = errors.Join(returned, owned.closePending())
			prepared = nil
		}
	}()
	prepared.pendingLease, err = store.lock(ctx, pendingSaveLease(token), false)
	if err != nil {
		return nil, err
	}
	err = store.withCreation(ctx, func() error {
		parent, leaf, err := store.parent(prepared.stage)
		if err != nil {
			return err
		}
		defer parent.Close()
		if err := parent.Mkdir(leaf, 0o700); err != nil {
			return err
		}
		prepared.root, err = cachefs.OpenPrivateChild(parent, leaf, false)
		return err
	})
	if err != nil {
		return nil, err
	}
	prepared.directory, err = cachefs.CaptureRoot(prepared.root)
	if err != nil {
		return nil, err
	}
	err = store.withCreation(ctx, func() error {
		var err error
		prepared.output, err = store.regular(prepared.stage+"/rootfs.img", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		return err
	})
	if err != nil {
		return nil, err
	}
	prepared.creator, err = store.captureCreated(prepared.stage+"/rootfs.img", prepared.output, 0o600)
	if err != nil {
		return nil, err
	}
	plan.SaveID, plan.Ref = token, request.Ref // Actual native pending correlation, never caller fields.
	report, err := convert(ctx, plan, produce, prepared.output)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := prepared.creator.File.CheckCreatedFile(prepared.root, "rootfs.img", prepared.output, 0o600); err != nil {
		return nil, err
	}
	record, err := Inspect(prepared.output)
	if err != nil {
		return nil, err
	}
	digest, err := savedRootDigest(ctx, prepared.output)
	if err != nil {
		return nil, err
	}
	stamp := time.Now().UTC()
	record.Ref, record.Digest, record.PlatformDigest, record.Arch = request.Ref, digest, digest, runtime.GOARCH
	record.ConvertedAt, record.UsedAt = stamp, stamp
	record.SavedHost = &SavedHostV1{Version: 1, HostID: store.domain.HostID, Ref: request.Ref, Digest: digest}
	record.SavedAliases = []string{request.Ref}
	prepared.record = record
	prepared.review = SaveReview{ID: token, Ref: request.Ref, Size: record.Size, Flagged: slices.Clone(report.Flagged), Excluded: slices.Clone(report.Excluded)}
	if err := prepared.review.Validate(request.Ref); err != nil {
		return nil, err
	}
	prepared.deadline = time.Now().Add(SaveLifetime)
	prepared.pendingHead, err = store.writeSavePending(ctx, prepared)
	if err != nil {
		return nil, err
	}
	prepared.timer = time.AfterFunc(SaveLifetime, func() { defer close(prepared.timerDone); _ = prepared.closePending() })
	complete = true
	return prepared, nil
}

// Review returns owned public metadata, without lending the pending root.
func (prepared *PreparedSave) Review() SaveReview {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	result := prepared.review
	result.Flagged, result.Excluded = slices.Clone(result.Flagged), slices.Clone(result.Excluded)
	return result
}

// ReviewPageV1 reads one immutable window without cloning the whole vector.
func (prepared *PreparedSave) ReviewPageV1(offset int) (SaveReviewPageV1, error) {
	if prepared == nil {
		return SaveReviewPageV1{}, saveProtocol("the original saved-image review owner is absent")
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.closed {
		return SaveReviewPageV1{}, saveProtocol("the original saved-image review has ended")
	}
	return prepared.review.PageV1(offset)
}

// Done closes after disposition and native cleanup have actually completed.
func (prepared *PreparedSave) Done() <-chan struct{} { return prepared.done }

// Close stops and joins expiry before dropping this creator's unkept root.
func (prepared *PreparedSave) Close() error {
	if prepared == nil {
		return nil
	}
	prepared.stopExpiry()
	return prepared.closePending()
}

func (prepared *PreparedSave) stopExpiry() {
	prepared.timerStop.Do(func() {
		if prepared.timer == nil {
			return
		}
		if prepared.timer.Stop() {
			close(prepared.timerDone)
		}
		<-prepared.timerDone
	})
}

func (prepared *PreparedSave) closePending() error {
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.closed {
		return prepared.closeErr
	}
	if !prepared.durable && prepared.root != nil && prepared.directory.Known() {
		owned := map[string]createdImageFile{}
		if prepared.creator.Descriptor != nil {
			owned["rootfs.img"] = prepared.creator
		}
		if prepared.metadata != nil {
			owned["image.json"] = *prepared.metadata
		}
		prepared.closeErr = prepared.store.cleanupStage(prepared.stage, prepared.root, prepared.directory, owned)
	}
	if prepared.output != nil {
		prepared.closeErr = errors.Join(prepared.closeErr, prepared.output.Close())
	}
	if prepared.metadata != nil && prepared.metadata.Descriptor != nil {
		prepared.closeErr = errors.Join(prepared.closeErr, prepared.metadata.Descriptor.Close())
	}
	if prepared.root != nil {
		prepared.closeErr = errors.Join(prepared.closeErr, prepared.root.Close())
	}
	if prepared.closeErr == nil && prepared.pendingHead.Ref.Known() {
		prepared.closeErr = prepared.store.removeSavePending(prepared.id, &prepared.pendingHead)
	}
	if prepared.pendingLease != nil {
		prepared.closeErr = errors.Join(prepared.closeErr, prepared.pendingLease.Close())
		if prepared.closeErr == nil {
			prepared.closeErr = prepared.store.retireSavePendingLease(prepared.id)
		}
	}
	prepared.closed = true
	close(prepared.done)
	return prepared.closeErr
}
