package host

import (
	"context"
	"errors"
	"runtime"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supply"
)

// PinInstalledAgentsV1 retains only the exact authenticated/protected client
// plan's existing canonical producer bytes. It performs no source refresh.
// The caller keeps this receipt until actual VM boot or full failed cleanup.
func PinInstalledAgentsV1(ctx context.Context, expected supply.ContentRef) (receipt *InstalledAgentsReceiptV1, returned error) {
	if err := image.ValidateAgentsContentV1(expected); err != nil {
		return nil, err
	}
	publication, err := beginRuntimePublication(ctx)
	if err != nil {
		return nil, err
	}
	receipt = &InstalledAgentsReceiptV1{publication: publication, done: make(chan struct{})}
	owned := receipt
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, owned.Close())
			receipt = nil
		}
	}()
	receipt.store, err = image.OpenHostExisting(publication.ctx, paths.ImagesDir(), publication.owner.domain())
	if err != nil {
		return receipt, err
	}
	receipt.disk, receipt.lease, err = receipt.store.PinAgentsV1(publication.ctx, expected, runtime.GOARCH)
	if err != nil {
		return receipt, err
	}
	go func() {
		select {
		case <-publication.ctx.Done():
			_ = owned.Close()
		case <-owned.done:
		}
	}()
	return receipt, nil
}
