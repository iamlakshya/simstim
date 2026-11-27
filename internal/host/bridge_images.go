package host

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

func init() { Features = append(Features, "images") }

func (b *bridge) handleImages(req proto.Request) bool {
	if req.Op != "image.ls" && req.Op != "image.pull" && req.Op != "image.install" && req.Op != "image.status" && req.Op != "image.rm" {
		return false
	}
	if err := CheckCaller(); err != nil {
		b.reply(req.ID, nil, err)
		return true
	}
	done, err := b.images.enter()
	if err != nil {
		b.reply(req.ID, nil, imageFailure(err))
		return true
	}
	defer done()
	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var result any
	switch req.Op {
	case "image.ls":
		var args struct{}
		if err = decodeRuntime(req.Args, &args); err == nil {
			var store *image.Store
			store, err = openImageInventory(ctx)
			if err == nil {
				result, err = store.List(ctx)
				err = errors.Join(err, store.Close())
			} else if errors.Is(err, os.ErrNotExist) {
				result, err = []image.Record{}, nil
			}
		}
	case "image.pull":
		var args image.Request
		if err = decodeRuntime(req.Args, &args); err == nil {
			result, err = b.images.pull(args, req.ID)
		}
	case "image.install":
		var args image.Install
		if err = decodeRuntime(req.Args, &args); err == nil {
			result, err = b.images.install(ctx, args, req.ID)
		}
	case "image.status":
		var args image.StatusRequest
		if err = decodeRuntime(req.Args, &args); err == nil {
			if (args.Token == "") == (args.Request == nil) {
				err = fmt.Errorf("image status needs exactly one progress token or cached-image request")
			} else if args.Request == nil {
				result, err = b.images.status(args.Token)
			} else {
				result, err = b.cachedImageStatus(ctx, *args.Request)
			}
		}
	case "image.rm":
		var args struct {
			Digest string `json:"digest"`
		}
		if err = decodeRuntime(req.Args, &args); err == nil {
			var store *image.Store
			store, err = b.images.open(ctx)
			if err == nil {
				owned, finish, contextErr := b.images.operationContext(ctx)
				if contextErr != nil {
					err = contextErr
				} else {
					err = store.Remove(owned, args.Digest, ImageReferences)
					finish()
				}
			}
		}
	default:
		err = fmt.Errorf("unknown image operation")
	}
	b.reply(req.ID, result, imageFailure(err))
	return true
}

func (b *bridge) cachedImageStatus(ctx context.Context, request image.Request) (image.CacheStatus, error) {
	if err := request.Validate(); err != nil {
		return image.CacheStatus{}, err
	}
	if request.Transfer != "" {
		return image.CacheStatus{}, fmt.Errorf("a cached image status cannot name a transfer")
	}
	if _, err := os.Lstat(paths.ImagesDir()); errors.Is(err, os.ErrNotExist) {
		return image.CacheStatus{}, nil
	} else if err != nil {
		return image.CacheStatus{}, err
	}
	store, err := b.images.open(ctx)
	if err != nil {
		return image.CacheStatus{}, err
	}
	owned, finish, err := b.images.operationContext(ctx)
	if err != nil {
		return image.CacheStatus{}, err
	}
	defer finish()
	return store.Cached(owned, request)
}
