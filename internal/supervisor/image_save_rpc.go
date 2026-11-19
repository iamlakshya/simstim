package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, proto.FeatureImageSave) }

type imageSaveJobV1 struct {
	root              *Supervisor
	client            *client
	selected          string
	token             string
	ref               string
	flagged, excluded int
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
	use               sync.Mutex
	source            *host.ImageSaveSource
	layer             *imageSaveLayerV1
	prepared          *image.PreparedSave
	closeOnce         sync.Once
	closeErr          error
	audited           bool
	watchDone         chan struct{}
}

func (s *Supervisor) imageSaveRequestV1(c *client, request proto.Request) bool {
	if !host.IsImageSaveOperationV1(request.Op) {
		return false
	}
	if request.Op == proto.FeatureImageSave {
		var args image.SaveArgsV1
		if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.Session) || !image.ValidSaveRef(args.Request.Ref) {
			c.reply(request.ID, nil, toProtoErr(imageSaveFailureV1("invalid original image-save declaration", nil)))
			return true
		}
		c.imageSaveMu.Lock()
		if c.imageSaveClosed || c.imageSave != nil {
			c.imageSaveMu.Unlock()
			clear(args.Request.Values)
			c.reply(request.ID, nil, toProtoErr(imageSaveFailureV1("this accepted connection already owns an image save", nil)))
			return true
		}
		root := s.owner()
		ctx, cancel := context.WithCancel(root.lifetimeContext())
		job := &imageSaveJobV1{root: root, client: c, selected: args.Session, ctx: ctx, cancel: cancel, done: make(chan struct{})}
		root.imageSaveMu.Lock()
		if root.imageSavesClosed {
			root.imageSaveMu.Unlock()
			c.imageSaveMu.Unlock()
			cancel()
			clear(args.Request.Values)
			c.reply(request.ID, nil, toProtoErr(imageSaveFailureV1("the original runtime is closing", nil)))
			return true
		}
		if root.imageSaves == nil {
			root.imageSaves = make(map[*imageSaveJobV1]struct{})
		}
		root.imageSaves[job] = struct{}{}
		root.imageSaveMu.Unlock()
		c.imageSave = job
		c.imageSaveMu.Unlock()
		go func() {
			review, err := job.prepare(args.Request)
			clear(args.Request.Values)
			close(job.done)
			if err != nil {
				err = errors.Join(err, job.Close())
			}
			c.reply(request.ID, review, toProtoErrOrNil(err))
		}()
		return true
	}
	if request.Op == "image.review" {
		var args image.SaveReviewArgsV1
		if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.Session) || args.Offset < 0 {
			c.reply(request.ID, nil, toProtoErr(imageSaveFailureV1("invalid original image-review page", nil)))
			return true
		}
		c.imageSaveMu.Lock()
		job := c.imageSave
		closed := c.imageSaveClosed
		c.imageSaveMu.Unlock()
		if job == nil || closed || job.selected != args.Session {
			c.reply(request.ID, nil, toProtoErr(imageSaveFailureV1("the image-review page has no original accepted source", nil)))
			return true
		}
		select {
		case <-job.done:
		default:
			c.reply(request.ID, nil, toProtoErr(imageSaveFailureV1("the original image review is still being prepared", nil)))
			return true
		}
		go func() {
			job.use.Lock()
			if job.prepared == nil || job.token != args.ID {
				job.use.Unlock()
				c.reply(request.ID, nil, toProtoErr(imageSaveFailureV1("the image-review token belongs to another source", nil)))
				return
			}
			var page image.SaveReviewPageV1
			err := job.source.Check(job.ctx)
			if err == nil {
				page, err = job.prepared.ReviewPageV1(args.Offset)
			}
			if err == nil {
				err = page.Validate(job.ref, job.token, args.Offset)
			}
			job.use.Unlock()
			c.reply(request.ID, page, toProtoErrOrNil(err))
		}()
		return true
	}
	var args image.SaveSelectionArgsV1
	if !api.ExactJSONFields(request.Args, args) || json.Unmarshal(request.Args, &args) != nil || args.Version != 1 || !api.ValidSessionID(args.Session) {
		c.reply(request.ID, nil, toProtoErr(imageSaveFailureV1("invalid original image-save selection", nil)))
		return true
	}
	c.imageSaveMu.Lock()
	job := c.imageSave
	closed := c.imageSaveClosed
	c.imageSaveMu.Unlock()
	if job == nil || closed || job.selected != args.Session {
		c.reply(request.ID, nil, toProtoErr(imageSaveFailureV1("the pending image does not belong to this accepted session connection", nil)))
		return true
	}
	select {
	case <-job.done:
	default:
		c.reply(request.ID, nil, toProtoErr(imageSaveFailureV1("the original image review is still being prepared", nil)))
		return true
	}
	go func() {
		job.use.Lock()
		if job.prepared == nil || job.token != args.Selection.ID {
			job.use.Unlock()
			c.reply(request.ID, nil, toProtoErr(imageSaveFailureV1("the pending image token is not owned by this connection", nil)))
			return
		}
		var result any
		var err error
		if request.Op == "image.keep" {
			if err = job.source.Check(job.ctx); err == nil {
				var kept image.SaveResult
				kept, err = job.prepared.Keep(job.ctx)
				result = kept
				if err == nil && !job.audited {
					job.audited = true
					job.root.audit.write("image_save", map[string]any{"ref": kept.Image.Ref, "digest": kept.Image.Digest, "flagged": job.flagged, "excluded": job.excluded})
				}
			}
		}
		job.use.Unlock()
		err = errors.Join(err, job.Close())
		c.reply(request.ID, result, toProtoErrOrNil(err))
	}()
	return true
}
func (job *imageSaveJobV1) prepare(request image.SaveRequest) (image.SaveReviewPageV1, error) {
	var err error
	job.source, err = job.root.captureImageSaveSourceV1(job.ctx, job.selected)
	if err != nil {
		return image.SaveReviewPageV1{}, err
	}
	job.layer, err = newImageSaveLayerV1(job.root)
	if err != nil {
		return image.SaveReviewPageV1{}, err
	}
	changes, err := job.layer.changes(job.ctx)
	if err != nil {
		return image.SaveReviewPageV1{}, errors.Join(err, job.layer.closeJoined())
	}
	job.prepared, err = job.source.Prepare(job.ctx, request, changes, job.layer.writeRoot)
	err = errors.Join(err, job.layer.closeJoined())
	if err != nil {
		return image.SaveReviewPageV1{}, err
	}
	page, err := job.prepared.ReviewPageV1(0)
	if err != nil {
		return image.SaveReviewPageV1{}, err
	}
	if err := page.Validate(request.Ref, "", 0); err != nil {
		return image.SaveReviewPageV1{}, err
	}
	job.token, job.ref = page.ID, request.Ref
	job.flagged, job.excluded = page.FlaggedTotal, page.ExcludedTotal
	// Expiry is the actual PreparedSave's joined native cleanup, not a timer
	// copied into a second manager. Source/OP close follows that SAME Done.
	job.watchDone = make(chan struct{})
	go func() { defer close(job.watchDone); <-job.prepared.Done(); <-job.done; _ = job.closeOwned(true) }()
	return page, nil
}

func (job *imageSaveJobV1) Close() error { return job.closeOwned(false) }
func (job *imageSaveJobV1) closeOwned(expiryOwner bool) error {
	if job == nil {
		return nil
	}
	job.cancel()
	<-job.done
	// If a prior cancellation could not establish native exit, keep the source
	// and its OP reachable in the original supervisor until its owner recovers.
	if job.layer != nil && job.layer.uncertain && !job.root.imageSavePhysicalExitV1() {
		return imageSaveFailureV1("the original image source retains uncertain native resources", nil)
	}
	job.closeOnce.Do(func() {
		job.use.Lock()
		defer job.use.Unlock()
		if job.layer != nil {
			job.closeErr = errors.Join(job.closeErr, job.layer.closeJoined())
		}
		if job.source != nil {
			job.closeErr = errors.Join(job.closeErr, job.source.Close())
		}
		job.client.imageSaveMu.Lock()
		if job.client.imageSave == job {
			job.client.imageSave = nil
		}
		job.client.imageSaveMu.Unlock()
		job.root.imageSaveMu.Lock()
		delete(job.root.imageSaves, job)
		job.root.imageSaveMu.Unlock()
	})
	if !expiryOwner && job.watchDone != nil {
		<-job.watchDone
	}
	return job.closeErr
}
func (c *client) closeImageSaveV1() {
	c.imageSaveMu.Lock()
	c.imageSaveClosed = true
	job := c.imageSave
	c.imageSaveMu.Unlock()
	if job != nil {
		_ = job.Close()
	}
}
func (s *Supervisor) cancelImageSavesV1() {
	root := s.owner()
	root.imageSaveMu.Lock()
	root.imageSavesClosed = true
	jobs := make([]*imageSaveJobV1, 0, len(root.imageSaves))
	for job := range root.imageSaves {
		jobs = append(jobs, job)
	}
	root.imageSaveMu.Unlock()
	for _, job := range jobs {
		job.cancel()
	}
}
func (s *Supervisor) joinImageSavesV1() error {
	root := s.owner()
	root.imageSaveMu.Lock()
	jobs := make([]*imageSaveJobV1, 0, len(root.imageSaves))
	for job := range root.imageSaves {
		jobs = append(jobs, job)
	}
	root.imageSaveMu.Unlock()
	var result error
	for _, job := range jobs {
		result = errors.Join(result, job.Close())
	}
	return result
}
