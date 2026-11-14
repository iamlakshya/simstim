package host

import (
	"context"
	"fmt"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

type imageJob struct {
	ctx               context.Context
	cancel            context.CancelFunc
	requestID         uint64
	started, complete bool
	progress          image.Progress
}

// Registration is in the bridge reader, before dispatch. Status/abort frames
// following a pull therefore always refer to the connection's owned job.
func (b *bridge) queueImagePull(request proto.Request) error {
	if err := CheckCaller(); err != nil {
		return err
	}
	var args image.Request
	if err := decodeRuntime(request.Args, &args); err != nil {
		return err
	}
	if err := args.Validate(); err != nil {
		return err
	}
	if args.Transfer == "" {
		return fmt.Errorf("image pull needs a transfer identifier")
	}
	s := &b.images
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("image connection has ended")
	}
	if s.jobs == nil {
		s.jobs = map[string]*imageJob{}
	}
	if s.jobs[args.Transfer] != nil {
		return fmt.Errorf("image transfer identifier is already in use")
	}
	active := len(s.transfers)
	for token, job := range s.jobs {
		if job.complete {
			delete(s.jobs, token)
		} else {
			active++
		}
	}
	if active >= 8 {
		return fmt.Errorf("this connection already has eight image operations")
	}
	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	s.jobs[args.Transfer] = &imageJob{ctx: ctx, cancel: cancel, requestID: request.ID, progress: image.Progress{Token: args.Transfer}}
	return nil
}

func (s *imageService) pull(args image.Request, requestID uint64) (image.Record, error) {
	s.mu.Lock()
	job := s.jobs[args.Transfer]
	if job == nil || job.requestID != requestID || job.started {
		s.mu.Unlock()
		return image.Record{}, fmt.Errorf("image pull does not own its queued transfer")
	}
	job.started = true
	s.mu.Unlock()
	defer func() { job.cancel(); s.mu.Lock(); job.complete = true; s.mu.Unlock() }()
	store, err := s.open(job.ctx)
	if err != nil {
		return image.Record{}, err
	}
	ctx, finish, err := s.operationContext(job.ctx)
	if err != nil {
		return image.Record{}, err
	}
	defer finish()
	return store.Prepare(ctx, args, &image.Registry{}, ConvertImage, func(progress image.Progress) { s.mu.Lock(); job.progress = progress; s.mu.Unlock() })
}
