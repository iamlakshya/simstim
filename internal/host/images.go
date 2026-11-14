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
	mu          sync.Mutex
	store       *image.Store
	transfers   map[string]*image.Transfer
	closed      bool
	active      sync.WaitGroup
	publication *runtimePublication
	closeOnce   sync.Once
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
func (s *imageService) install(ctx context.Context, args image.Install) (any, error) {
	if args.Action == "abort" {
		s.mu.Lock()
		job := s.jobs[args.Token]
		if job != nil {
			job.cancel()
			delete(s.jobs, args.Token)
		}
		s.mu.Unlock()
		if job != nil {
			return nil, nil
		}
	}
	if args.Action == "begin" {
		if args.Request == nil || args.Token != "" || args.Digest != "" || args.Offset != 0 || len(args.Data) != 0 {
			return nil, fmt.Errorf("invalid image begin request")
		}
		if err := args.Request.Validate(); err != nil {
			return nil, err
		}
	} else if args.Action != "chunk" && args.Action != "commit" && args.Action != "abort" {
		return nil, fmt.Errorf("unknown image install action")
	}
	store, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	ctx, finish, err := s.operationContext(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	if args.Action == "begin" {
		if args.Request == nil || args.Token != "" || args.Digest != "" || args.Offset != 0 || len(args.Data) != 0 {
			return nil, fmt.Errorf("invalid image begin request")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.transfers) >= 8 {
			return nil, fmt.Errorf("this connection already has eight image transfers")
		}
		transfer, err := store.BeginContext(ctx, *args.Request)
		if err != nil {
			return nil, err
		}
		progress := transfer.Progress()
		s.transfers[progress.Token] = transfer
		return progress, nil
	}
	if args.Request != nil {
		return nil, fmt.Errorf("only image begin accepts a request")
	}
	s.mu.Lock()
	transfer := s.transfers[args.Token]
	s.mu.Unlock()
	if transfer == nil {
		return nil, fmt.Errorf("image transfer does not belong to this connection")
	}
	switch args.Action {
	case "chunk":
		if err := transfer.WriteAt(ctx, args.Digest, args.Offset, args.Data); err != nil {
			return nil, err
		}
		return transfer.Progress(), nil
	case "commit", "abort":
		s.mu.Lock()
		delete(s.transfers, args.Token)
		s.mu.Unlock()
		if args.Digest != "" || args.Offset != 0 || len(args.Data) != 0 {
			transfer.Close()
			return nil, fmt.Errorf("invalid image completion request")
		}
		if args.Action == "abort" {
			return nil, transfer.Close()
		}
		return transfer.Commit(ctx, ConvertImage)
	default:
		return nil, fmt.Errorf("unknown image install action")
	}
}

func (s *imageService) status(token string) (image.Progress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job := s.jobs[token]; job != nil {
		return job.progress, nil
	}
	transfer := s.transfers[token]
	if transfer == nil {
		return image.Progress{}, fmt.Errorf("image transfer does not belong to this connection")
	}
	return transfer.Progress(), nil
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
