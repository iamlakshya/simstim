package host

import (
	"context"
	"runtime"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/supply"
)

// PinInstalledAgentsV1 retains only the exact authenticated/protected client
// plan's existing canonical producer bytes. It performs no source refresh.
// The caller keeps this receipt until actual VM boot or full failed cleanup.
// A nonnil receipt on error still owns retryable original cleanup.
func PinInstalledAgentsV1(ctx context.Context, expected supply.ContentRef) (receipt *InstalledAgentsReceiptV1, returned error) {
	if err := image.ValidateAgentsContentV1(expected); err != nil {
		return nil, err
	}
	publication, err := beginRuntimePublication(ctx)
	if err != nil {
		return nil, err
	}
	return pinInstalledAgentsPublicationV1(expected, publication)
}

// PinOriginalBootAgentsV1 pins only the agent image from this actual original
// boot receipt. A raw ContentRef or expected-spec context cannot issue it.
func PinOriginalBootAgentsV1(ctx context.Context) (*InstalledAgentsReceiptV1, error) {
	receipt := OriginalVMNeedFromContext(ctx)
	if receipt == nil || receipt.need == nil {
		return nil, vmNeedError("the agents image has no original native boot selection")
	}
	publication, err := beginBootAssetPublication(ctx, receipt.boot)
	if err != nil {
		return nil, err
	}
	return pinInstalledAgentsPublicationV1(receipt.need.Agents, publication)
}

func pinInstalledAgentsPublicationV1(expected supply.ContentRef, publication *runtimePublication) (receipt *InstalledAgentsReceiptV1, returned error) {
	if publication == nil {
		return nil, agentsBuildFailure("the pinned agents image has no original native publication")
	}
	receipt = &InstalledAgentsReceiptV1{publication: publication, done: make(chan struct{})}
	owned := receipt
	defer finishAgentsFactoryV1(&receipt, &returned)
	var err error
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
