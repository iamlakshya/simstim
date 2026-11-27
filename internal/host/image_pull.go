package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

type imageJob struct {
	ctx                      context.Context
	cancel                   context.CancelFunc
	requestID                uint64
	request                  image.Request
	correlation, kind, token string
	started, complete        bool
	progress                 image.Progress
	done                     chan struct{}
	finishOnce               sync.Once
	operation                sync.Mutex
	transfer                 *image.Transfer
}

// queueImageOperationV1 runs in the original bridge reader before dispatch.
// A following abort can cancel the original request even if begin's random
// token reply was lost or its handler has not yet been scheduled.
func (b *bridge) queueImageOperationV1(request proto.Request) (func(), error) {
	var args image.Request
	kind := ""
	switch request.Op {
	case "image.pull":
		if err := decodeRuntime(request.Args, &args); err != nil {
			return nil, err
		}
		kind = "pull"
	case "image.install":
		var install image.Install
		if err := decodeRuntime(request.Args, &install); err != nil {
			return nil, err
		}
		if install.Action != "begin" {
			return nil, nil
		}
		if err := checkImageBeginV1(install); err != nil {
			return nil, err
		}
		args = *install.Request
		kind = "begin"
	default:
		return nil, nil
	}
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	if err := args.Validate(); err != nil {
		return nil, err
	}
	if args.Transfer == "" {
		return nil, fmt.Errorf("image %s needs an original transfer identifier", kind)
	}
	s := &b.images
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("image connection has ended")
	}
	if s.jobs == nil {
		s.jobs = map[string]*imageJob{}
	}
	if s.jobs[args.Transfer] != nil {
		return nil, fmt.Errorf("image transfer identifier is already in use")
	}
	active := map[*imageJob]bool{}
	for token, job := range s.jobs {
		if job.complete {
			delete(s.jobs, token)
		} else {
			active[job] = true
		}
	}
	if len(active) >= 8 {
		return nil, fmt.Errorf("this connection already has eight image operations")
	}
	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if s.connection == nil {
		s.connection = ctx
	}
	ctx, cancel := context.WithCancel(ctx)
	job := &imageJob{ctx: ctx, cancel: cancel, requestID: request.ID, request: args, correlation: args.Transfer, kind: kind, progress: image.Progress{Token: args.Transfer}, done: make(chan struct{})}
	s.jobs[args.Transfer] = job
	s.active.Add(1)
	return func() { s.finishImageHandlerV1(job) }, nil
}
func (s *imageService) finishImageHandlerV1(job *imageJob) {
	job.finishOnce.Do(func() {
		s.mu.Lock()
		if job.kind == "pull" || job.transfer == nil {
			job.complete = true
			job.cancel()
		}
		close(job.done)
		s.mu.Unlock()
		s.active.Done()
	})
}
func (s *imageService) claimImageHandlerV1(args image.Request, requestID uint64, kind string) (*imageJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.jobs[args.Transfer]
	if job == nil || job.requestID != requestID || job.kind != kind || job.started {
		return nil, fmt.Errorf("image operation does not own its original queued transfer")
	}
	expected, _ := json.Marshal(job.request)
	actual, _ := json.Marshal(args)
	if !bytes.Equal(expected, actual) {
		return nil, fmt.Errorf("image operation changed its original queued pins")
	}
	job.started = true
	return job, nil
}
func (s *imageService) pull(args image.Request, requestID uint64) (image.Record, error) {
	job, err := s.claimImageHandlerV1(args, requestID, "pull")
	if err != nil {
		return image.Record{}, err
	}
	defer s.finishImageHandlerV1(job)
	if err := job.ctx.Err(); err != nil {
		return image.Record{}, err
	}
	store, err := s.open(job.ctx)
	if err != nil {
		return image.Record{}, err
	}
	ctx, finish, err := s.operationContext(job.ctx)
	if err != nil {
		return image.Record{}, err
	}
	defer finish()
	args.Transfer = "" // immutable OCI selection never retains RPC correlation.
	return store.Prepare(ctx, args, &image.Registry{}, ConvertImage, func(progress image.Progress) {
		s.mu.Lock()
		progress.Token = job.correlation
		job.progress = progress
		s.mu.Unlock()
	})
}
