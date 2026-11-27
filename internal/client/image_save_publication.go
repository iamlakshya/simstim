package client

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

type imageSavePublicationKey struct{}

// save is the only image-save value producer. Its map remains private and
// immutable through the actual common physical writer. A retry after any
// publication attempt cannot construct a second pending save.
func (o *imageSaveOperation) save(ctx context.Context) (image.SaveReview, error) {
	if err := o.check(ctx); err != nil {
		return image.SaveReview{}, err
	}
	o.mu.Lock()
	if o.reviewFailure != nil {
		err := o.reviewFailure
		o.mu.Unlock()
		return image.SaveReview{}, err
	}
	if o.review != nil {
		result := cloneImageSaveReview(*o.review)
		o.mu.Unlock()
		if _, err := o.checkValuesLocal(ctx); err != nil {
			return image.SaveReview{}, err
		}
		return result, nil
	}
	if o.closed || o.busy || !o.fetched || o.attempted && o.reviewProgress == nil {
		o.mu.Unlock()
		return image.SaveReview{}, imageSaveAuthorityError()
	}
	initial := !o.attempted
	o.busy, o.publishing = true, initial
	o.mu.Unlock()
	defer func() { o.mu.Lock(); o.busy, o.publishing = false, false; o.mu.Unlock() }()
	if initial {
		if err := o.restrictSession(ctx); err != nil {
			return image.SaveReview{}, err
		}
		owned, release, err := o.begin(ctx, "save image")
		if err != nil {
			return image.SaveReview{}, err
		}
		args := image.SaveArgsV1{Version: 1, Session: o.id, Request: image.SaveRequest{Ref: o.ref, Replace: o.replace, Values: o.values}}
		publication := context.WithValue(owned, imageSavePublicationKey{}, o)
		o.mu.Lock()
		o.attempted = true
		o.mu.Unlock()
		var raw json.RawMessage
		if err := o.conn.callWithSent(publication, "image.save", args, &raw, release); err != nil {
			return image.SaveReview{}, err
		}
		if err := o.acceptImageSavePage(raw, true); err != nil {
			return image.SaveReview{}, err
		}
		o.mu.Lock()
		o.publishing = false
		o.mu.Unlock()
	}
	return o.collectImageSaveReview(ctx)
}

func cloneImageSaveReview(source image.SaveReview) image.SaveReview {
	source.Flagged = slices.Clone(source.Flagged)
	source.Excluded = slices.Clone(source.Excluded)
	return source
}

func (o *imageSaveOperation) beforeRequest(ctx context.Context, c *Conn, request proto.Request, raw any) error {
	args, ok := raw.(image.SaveArgsV1)
	if !ok || o == nil || c != o.conn || request.Op != "image.save" || args.Version != 1 || args.Session != o.id || args.Request.Ref != o.ref || args.Request.Replace != o.replace {
		return imageSaveAuthorityError()
	}
	o.mu.Lock()
	valid := o.busy && o.publishing && o.attempted && o.fetched && !o.closed && sameSelectedSecretValues(args.Request.Values, o.values)
	o.mu.Unlock()
	if !valid {
		return imageSaveAuthorityError()
	}
	tier, err := o.checkValuesLocal(ctx)
	if err != nil {
		return err
	}
	if err := c.requireSecretFeature(len(o.names) != 0); err != nil {
		return err
	}
	values := context.WithValue(ctx, imageSaveValuePurposeKey{}, o)
	if err := recordSecretNames(values, c.Host, c.HostID(), tier, o.values); err != nil {
		return err
	}
	_, err = o.checkValuesLocal(ctx)
	return err
}
