package client

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

// ImageSaveOptions contains the original local review and replacement choices.
// Secret values are selected privately by the original operation.
type ImageSaveOptions struct{ Replace, AllowFlagged, Terminal bool }

// ImageSave retains one prepared image and its original request, session and
// accepted connection. Close is required before closing the caller's request.
type ImageSave struct {
	mu              sync.Mutex
	original        context.Context
	guard           *imageSaveOperation
	options         ImageSaveOptions
	review          image.SaveReview
	result          image.SaveResult
	prepared        bool
	allowed         bool
	kept, pinned    bool
	keepAttempted   bool
	closed          bool
	cleanupDeadline time.Time
}

// PrepareImageSave selects session values and prepares one review. Consent
// retries retain the same private facade and cannot publish another save.
// Any returned operation must be closed, including when an error is returned.
func PrepareImageSave(ctx context.Context, c *Conn, id, ref string, options ImageSaveOptions) (_ *ImageSave, returned error) {
	defer func() { returned = imageSaveClientError(returned) }()
	if !image.ValidSaveRef(ref) {
		return nil, &UserError{Code: "bad_image_ref", Kind: KindUsage,
			Msg: "invalid saved image name", Hint: "Use 1 to 64 lower-case letters, numbers, dots, underscores or hyphens, starting with a letter or number."}
	}
	if c == nil || !c.HasFeature(proto.FeatureImageSave) {
		return nil, &UserError{Code: "feature_missing", Kind: KindFailed,
			Msg: "the host helper cannot save sandbox images", Hint: "Upgrade the host helper before saving an image."}
	}
	guard, err := newImageSaveOperation(ctx, c, id, ref, options.Replace)
	if err != nil {
		return nil, err
	}
	guard.mu.Lock()
	operation, ok := guard.facade.(*ImageSave)
	if guard.facade == nil {
		operation = &ImageSave{original: ctx, guard: guard, options: options, allowed: options.AllowFlagged}
		guard.facade = operation
	} else if !ok {
		guard.mu.Unlock()
		return nil, grantOperationError()
	}
	guard.mu.Unlock()
	if operation.options != options {
		return nil, grantOperationError()
	}
	if err := operation.prepare(ctx); err != nil {
		return operation, err
	}
	return operation, nil
}

func (s *ImageSave) prepare(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return grantOperationError()
	}
	if err := s.guard.check(ctx); err != nil {
		return err
	}
	if s.prepared {
		return nil
	}
	s.guard.mu.Lock()
	attempted := s.guard.attempted
	s.guard.mu.Unlock()
	if !attempted {
		if err := s.guard.prepare(ctx, s.options.Terminal); err != nil {
			return err
		}
	}
	review, err := s.guard.save(ctx)
	if err != nil {
		return err
	}
	// The original response token remains available for drop even when its
	// remaining metadata fails validation. It can never become a path.
	s.review = review
	if err := review.Validate(s.guard.ref); err != nil {
		return err
	}
	s.review.Flagged = slices.Clone(review.Flagged)
	s.review.Excluded = slices.Clone(review.Excluded)
	s.prepared = true
	return nil
}

// Review returns a copy of the findings, never scanner input or root bytes.
func (s *ImageSave) Review() image.SaveReview {
	s.mu.Lock()
	defer s.mu.Unlock()
	review := s.review
	review.Flagged = slices.Clone(review.Flagged)
	review.Excluded = slices.Clone(review.Excluded)
	return review
}

// AllowFlagged records the live answer after the caller displays the review.
func (s *ImageSave) AllowFlagged() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed && !s.kept {
		s.allowed = true
	}
}

// Keep publishes only the original prepared ID, then records its same-host
// pin under the original accepted Source. A failed local pin remains retryable.
func (s *ImageSave) Keep(ctx context.Context) (_ image.SaveResult, returned error) {
	defer func() { returned = imageSaveClientError(returned) }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || !s.prepared {
		return image.SaveResult{}, grantOperationError()
	}
	if err := s.guard.check(ctx); err != nil {
		return image.SaveResult{}, err
	}
	if len(s.review.Flagged) != 0 && !s.allowed {
		return image.SaveResult{}, &UserError{Code: "image_flagged", Kind: KindRefused,
			Msg: "the image has changes that run code at login or at start", Hint: "Read the flagged files, then save again with --allow-flagged."}
	}
	if !s.kept {
		if s.keepAttempted {
			return image.SaveResult{}, grantOperationError()
		}
		owned, release, err := s.guard.begin(ctx, "keep image save")
		if err != nil {
			return image.SaveResult{}, err
		}
		args := image.SaveSelectionArgsV1{Version: 1, Session: s.guard.id, Selection: image.SaveSelection{ID: s.review.ID}}
		var result image.SaveResult
		s.keepAttempted = true
		if err := s.guard.conn.callWithSent(owned, "image.keep", args, &result, release); err != nil {
			return image.SaveResult{}, err
		}
		if err := result.Validate(s.guard.ref, s.guard.conn.HostID()); err != nil {
			return image.SaveResult{}, err
		}
		if result.Image.Size != s.review.Size {
			return image.SaveResult{}, imageSaveProtocolError()
		}
		s.result, s.kept = cloneImageSaveResult(result), true
	}
	if !s.pinned {
		owned, release, err := s.guard.begin(ctx, "record saved image")
		if err != nil {
			return image.SaveResult{}, err
		}
		err = updateImageLock(owned, func(lock *image.Lock) error {
			return recordSavedImagePin(lock, s.result, s.guard.ref, s.guard.conn.HostID(), s.guard.conn.Host.Name, s.options.Replace, time.Now())
		})
		release()
		if err != nil {
			return image.SaveResult{}, err
		}
		s.pinned = true
	}
	return cloneImageSaveResult(s.result), nil
}

// Close drops only this operation's unkept save ID. Cancellation uses one
// fixed cleanup deadline on the same accepted connection and original request.
func (s *ImageSave) Close() (returned error) {
	defer func() { returned = imageSaveClientError(returned) }()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if s.review.ID == "" && !s.kept {
		s.review.ID = s.guard.cleanupSaveID()
	}
	if s.kept || s.review.ID == "" {
		s.closed = true
		return nil
	}
	if len(s.review.ID) != 32 || !supply.IsHex64(s.review.ID+s.review.ID) {
		return imageSaveProtocolError()
	}
	if s.cleanupDeadline.IsZero() {
		s.cleanupDeadline = time.Now().Add(5 * time.Second)
	}
	cleanup, cancel := context.WithDeadline(context.WithoutCancel(s.original), s.cleanupDeadline)
	defer cancel()
	owned, release, err := s.guard.beginCleanup(cleanup)
	if err != nil {
		return err
	}
	args := image.SaveSelectionArgsV1{Version: 1, Session: s.guard.id, Selection: image.SaveSelection{ID: s.review.ID}}
	if err := s.guard.conn.callWithSent(owned, "image.drop", args, nil, release); err != nil {
		return err
	}
	s.closed = true
	return nil
}

func cloneImageSaveResult(result image.SaveResult) image.SaveResult {
	result.Image.SavedAliases = slices.Clone(result.Image.SavedAliases)
	if result.Image.SavedHost != nil {
		identity := *result.Image.SavedHost
		result.Image.SavedHost = &identity
	}
	return result
}
