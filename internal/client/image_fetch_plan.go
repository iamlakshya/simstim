package client

import (
	"context"
	"reflect"
	"slices"

	"github.com/iamlakshya/simstim/internal/image"
)

func prepareImageCopyPlan(ctx context.Context, c *Conn, request image.Request, documents []image.Document, layers []image.Layer) error {
	p, _ := ctx.Value(imageFetchPublicationKeyV1{}).(*imageFetchIntentV1)
	if p == nil {
		return nil
	}
	if err := p.confirmed(ctx, c); err != nil {
		return err
	}
	if !reflect.DeepEqual(request, p.resolved.request) {
		return imageFetchIntentError()
	}
	byDigest := make(map[string][]byte, len(documents))
	blobs := make(map[string]int64, len(layers)+len(documents))
	for _, document := range documents {
		if document.Digest != request.Digest && document.Digest != request.PlatformDigest {
			return imageFetchIntentError()
		}
		if _, repeated := byDigest[document.Digest]; repeated {
			return imageFetchIntentError()
		}
		byDigest[document.Digest] = document.Data
		blobs[document.Digest] = int64(len(document.Data))
	}
	canonical, err := image.CheckDocuments(request, byDigest)
	if err != nil {
		return err
	}
	if !slices.Equal(canonical, layers) {
		return imageFetchIntentError()
	}
	for _, layer := range canonical {
		if size, known := blobs[layer.Digest]; known && size != layer.Size {
			return imageFetchIntentError()
		}
		blobs[layer.Digest] = layer.Size
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.usedLaptop || p.blobs != nil {
		return imageFetchIntentError()
	}
	p.blobs, p.offsets = blobs, make(map[string]int64, len(blobs))
	return nil
}
