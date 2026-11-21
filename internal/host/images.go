package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/runtimecache"
)

type imageService struct {
	openMu      sync.Mutex
	connection  context.Context
	mu          sync.Mutex
	store       *image.Store
	transfers   map[string]*image.Transfer
	closed      bool
	active      sync.WaitGroup
	publication *runtimePublication
	closeMu     sync.Mutex
	closeErr    error
	jobs        map[string]*imageJob
}

func (s *imageService) enter() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("image connection has ended")
	}
	s.active.Add(1)
	return s.active.Done, nil
}
func checkImageBeginV1(args image.Install) error {
	if args.Request == nil || args.Request.Transfer == "" || args.Token != "" || args.Digest != "" || args.Offset != 0 || len(args.Data) != 0 {
		return fmt.Errorf("invalid correlated image begin request")
	}
	return args.Request.Validate()
}
func (s *imageService) install(ctx context.Context, args image.Install, requestID uint64) (any, error) {
	if args.Action == "begin" {
		return s.beginImageV1(args, requestID)
	}
	if args.Action != "chunk" && args.Action != "commit" && args.Action != "abort" {
		return nil, fmt.Errorf("unknown image install action")
	}
	if args.Request != nil || args.Token == "" {
		return nil, fmt.Errorf("only image begin accepts a request; later phases need its original token")
	}
	if args.Action != "chunk" && (args.Digest != "" || args.Offset != 0 || len(args.Data) != 0) {
		return nil, fmt.Errorf("invalid image completion request")
	}
	s.mu.Lock()
	job := s.jobs[args.Token]
	if job != nil && args.Action == "abort" {
		job.cancel()
	}
	s.mu.Unlock()
	if job == nil {
		return nil, fmt.Errorf("image transfer does not belong to this connection")
	}
	if args.Action == "abort" {
		// Join the actual begin/pull handler, including a request cancelled before
		// it ran. Never delete the correlation before its creator has finished.
		<-job.done
		job.operation.Lock()
		defer job.operation.Unlock()
		var err error
		if job.transfer != nil {
			err = job.transfer.Close()
		}
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		job.complete = true
		if s.jobs[job.correlation] == job {
			delete(s.jobs, job.correlation)
		}
		if job.token != "" {
			if s.jobs[job.token] == job {
				delete(s.jobs, job.token)
			}
			if s.transfers[job.token] == job.transfer {
				delete(s.transfers, job.token)
			}
		}
		s.mu.Unlock()
		return nil, nil
	}
	job.operation.Lock()
	defer job.operation.Unlock()
	s.mu.Lock()
	complete := job.complete
	transfer := job.transfer
	token := job.token
	s.mu.Unlock()
	if job.kind != "begin" || complete || transfer == nil || args.Token != token {
		return nil, fmt.Errorf("image phase does not own an active server transfer token")
	}
	if err := job.ctx.Err(); err != nil {
		return nil, err
	}
	owned, finish, err := s.operationContext(job.ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	if args.Action == "chunk" {
		if err := transfer.WriteAt(owned, args.Digest, args.Offset, args.Data); err != nil {
			return nil, err
		}
		progress := transfer.Progress()
		s.mu.Lock()
		job.progress = progress
		s.mu.Unlock()
		return progress, nil
	}
	record, err := transfer.Commit(owned, ConvertImage)
	// Commit returns only after conversion and transfer cleanup. An abort waits
	// on this same operation mutex before removing either connection alias.
	s.mu.Lock()
	job.complete = !errors.Is(err, image.ErrTransferCleanupPending)
	if job.complete && s.transfers[token] == transfer {
		delete(s.transfers, token)
	}
	s.mu.Unlock()
	job.cancel()
	return record, err
}
func (s *imageService) beginImageV1(args image.Install, requestID uint64) (any, error) {
	if err := checkImageBeginV1(args); err != nil {
		return nil, err
	}
	job, err := s.claimImageHandlerV1(*args.Request, requestID, "begin")
	if err != nil {
		return nil, err
	}
	defer s.finishImageHandlerV1(job)
	job.operation.Lock()
	defer job.operation.Unlock()
	if err := job.ctx.Err(); err != nil {
		return nil, err
	}
	store, err := s.open(job.ctx)
	if err != nil {
		return nil, err
	}
	owned, finish, err := s.operationContext(job.ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	request := *args.Request
	request.Transfer = ""
	transfer, err := store.BeginContext(owned, request)
	if err != nil {
		return nil, err
	}
	progress := transfer.Progress()
	s.mu.Lock()
	if previous := s.jobs[progress.Token]; previous != nil && previous != job {
		s.mu.Unlock()
		return nil, errors.Join(fmt.Errorf("random image token collides with an existing connection operation"), transfer.Close())
	}
	job.transfer, job.token, job.progress = transfer, progress.Token, progress
	s.transfers[progress.Token] = transfer
	s.jobs[progress.Token] = job
	cancelled := job.ctx.Err()
	s.mu.Unlock()
	if cancelled != nil {
		return nil, cancelled
	}
	return progress, nil
}
func (s *imageService) status(token string) (image.Progress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job := s.jobs[token]; job != nil {
		return job.progress, nil
	}
	return image.Progress{}, fmt.Errorf("image transfer does not belong to this connection")
}

func imageFailure(err error) error {
	if err == nil {
		return nil
	}
	var pe *proto.Error
	if errors.As(err, &pe) {
		return pe
	}
	var metadata *runtimecache.ImageDefaultsUnavailableError
	if errors.As(err, &metadata) {
		return UserError("runtime_image_metadata", metadata.Error(), "Update the client and run host upgrade to refresh its default-image metadata.")
	}
	return UserError("image", err.Error(), "Check the host with host doctor, then pull the image again.")
}

// ImageReferences reads trusted host manifests, including stopped sessions.
func ImageReferences() (map[string]string, error) {
	used := map[string]string{}
	manifests, err := trustedSessionManifests()
	if err != nil {
		return nil, err
	}
	for _, manifest := range manifests {
		if err := addImageReferences(used, manifest); err != nil {
			return nil, err
		}
	}
	return used, nil
}

func addImageReferences(used map[string]string, manifest Manifest) error {
	if manifest.Run.Image != "" {
		if !image.ValidDigest(manifest.Run.Image) {
			return fmt.Errorf("invalid trusted image reference")
		}
		used[manifest.Run.Image] = manifest.ID
	}
	need, known, err := protectedNeedReferences(manifest)
	if err != nil {
		return err
	}
	if known {
		used[need.Image.PlatformDigest] = manifest.ID
	}
	return nil
}

// PruneImages removes images outside the reference and age retention rules.
func PruneImages(ctx context.Context) (removed []string, returned error) {
	defer func() { returned = imageFailure(returned) }()
	if err := CheckCaller(); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(paths.ImagesDir()); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	publication, err := beginRuntimePublication(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, publication.close()) }()
	ctx = publication.ctx
	store, err := image.OpenHost(ctx, paths.ImagesDir(), publication.owner.domain(), publication.gate)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, store.Close()) }()
	return store.Prune(ctx, ImageReferences, func() (map[string]bool, error) { return runtimeImageDefaults(ctx) }, time.Now())
}

func runtimeImageDefaults(ctx context.Context) (map[string]bool, error) {
	defaults := map[string]bool{}
	owner, ownerErr := placementForCall()
	if ownerErr != nil {
		return nil, &runtimecache.ImageDefaultsUnavailableError{Err: ownerErr}
	}
	store, err := runtimecache.OpenHostReadOnly(ctx, paths.ArtefactsDir(), owner.domain())
	if err != nil {
		return nil, &runtimecache.ImageDefaultsUnavailableError{Err: err}
	}
	defer store.Close()
	items, err := store.ImageDefaults(ctx, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		defaults[item.Digest] = true
	}
	return defaults, nil
}
