package client

import (
	"context"
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

type imageSaveReviewProgress struct {
	collector image.SaveReviewCollectorV1
	next      int
	done      bool
}

func (o *imageSaveOperation) failImageSavePage(err error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.reviewFailure == nil {
		o.reviewFailure = err
	}
	return o.reviewFailure
}

func (o *imageSaveOperation) acceptImageSavePage(raw []byte, initial bool) error {
	var page image.SaveReviewPageV1
	if !api.ExactJSONFields(raw, page) || json.Unmarshal(raw, &page) != nil {
		return o.failImageSavePage(&proto.Error{Code: "image_protocol", Message: "the original saved-image review page is not a closed object", Hint: "Upgrade the helper and repeat image save on the original host."})
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || !o.busy || !o.attempted || initial != (o.reviewProgress == nil) {
		return imageSaveAuthorityError()
	}
	progress := o.reviewProgress
	if initial {
		progress = &imageSaveReviewProgress{}
	}
	// The canonical collector checks original ID/ref/size/totals/offset,
	// complete encoded page size and flagged-then-excluded order.
	if err := progress.collector.Append(page, o.ref); err != nil {
		o.reviewFailure = err
		return err
	}
	progress.next, progress.done = page.Next, page.Done
	if initial {
		// Retain this actual validated first ACK for fixed drop even when
		// later paging, current permissions or cancellation prevent keep.
		o.pendingID, o.reviewProgress = page.ID, progress
	}
	if progress.done {
		result, err := progress.collector.Review()
		if err != nil {
			o.reviewFailure = err
			return err
		}
		copy := cloneImageSaveReview(result)
		o.review = &copy
	}
	return nil
}

func (o *imageSaveOperation) collectImageSaveReview(ctx context.Context) (image.SaveReview, error) {
	for {
		if _, err := o.checkValuesLocal(ctx); err != nil {
			return image.SaveReview{}, err
		}
		o.mu.Lock()
		progress, id := o.reviewProgress, o.pendingID
		if o.reviewFailure != nil || progress == nil || id == "" || !o.busy || o.closed {
			err := o.reviewFailure
			o.mu.Unlock()
			if err != nil {
				return image.SaveReview{}, err
			}
			return image.SaveReview{}, imageSaveAuthorityError()
		}
		if progress.done {
			if o.review == nil {
				o.mu.Unlock()
				return image.SaveReview{}, imageSaveAuthorityError()
			}
			result := cloneImageSaveReview(*o.review)
			o.mu.Unlock()
			if _, err := o.checkValuesLocal(ctx); err != nil {
				return image.SaveReview{}, err
			}
			return cloneImageSaveReview(result), nil
		}
		offset := progress.next
		o.mu.Unlock()
		owned, release, err := o.begin(ctx, "read image save review")
		if err != nil {
			return image.SaveReview{}, err
		}
		args := image.SaveReviewArgsV1{Version: 1, Session: o.id, ID: id, Offset: offset}
		var raw json.RawMessage
		if err := o.conn.callWithSent(owned, "image.review", args, &raw, release); err != nil {
			return image.SaveReview{}, err
		}
		if err := o.acceptImageSavePage(raw, false); err != nil {
			return image.SaveReview{}, err
		}
	}
}

// cleanupSaveID reveals only the original validated pending token. It reads
// no settings/values, renews no Source and authorises no keep or connection.
func (o *imageSaveOperation) cleanupSaveID() string {
	if o == nil {
		return ""
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.pendingID != "" {
		return o.pendingID
	}
	if o.review != nil {
		return o.review.ID
	}
	return ""
}
