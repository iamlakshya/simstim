package client

import (
	"context"
	"fmt"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

func checkImagePinMovement(ctx context.Context, request image.Request, options ImageOptions) error {
	if options.Update {
		return ctx.Err()
	}
	lock, err := image.ReadLock(ctx, ConfigDir())
	if err != nil {
		return err
	}
	for _, pin := range lock.Images {
		if pin.HostID == "" && pin.Ref == request.Ref && pin.Digest != request.Digest {
			return &UserError{Msg: fmt.Sprintf("%s now points at %s, and images.lock pins %s", CleanLine(request.Ref), request.Digest, pin.Digest), Hint: "Run image pull with --update after checking the new image.", Code: "image_moved", Kind: HostKind("image_moved")}
		}
	}
	return ctx.Err()
}

// applyImagePolicyReceipt runs inside the existing pin writer's exclusive
// lock. The caller must create/check the exact pin and platform first.
func applyImagePolicyReceipt(lock *image.Lock, request image.Request, receipt imagePolicyReceipt) error {
	if receipt.identity == "" && receipt.policy == "" && receipt.digest == "" {
		return nil
	}
	if request.SavedHost != nil || receipt.request.SavedHost != nil || receipt.request.Ref != request.Ref || receipt.request.Digest != request.Digest || receipt.request.PlatformDigest != request.PlatformDigest || receipt.request.Arch != request.Arch || receipt.identity == "" || !supply.IsHex64(receipt.policy) || receipt.digest != request.Digest && receipt.digest != request.PlatformDigest {
		return imagePolicyFailure(request, "has no matching original policy verification")
	}
	for i := range lock.Images {
		pin := &lock.Images[i]
		if pin.HostID != "" || pin.Ref != request.Ref || pin.Digest != request.Digest {
			continue
		}
		if receipt.digest == request.Digest {
			pin.SignedBy, pin.Policy = receipt.identity, receipt.policy
			return nil
		}
		for j := range pin.Platforms {
			platform := &pin.Platforms[j]
			if platform.Arch == request.Arch && platform.Digest == request.PlatformDigest {
				platform.SignedBy, platform.Policy = receipt.identity, receipt.policy
				return nil
			}
		}
	}
	return fmt.Errorf("the image pin changed before its signature verification could be recorded")
}

func recordImagePolicyReceipt(ctx context.Context, request image.Request, receipt imagePolicyReceipt) error {
	if receipt.identity == "" {
		return ctx.Err()
	}
	if err := revalidateRetainedImagePolicy(ctx); err != nil {
		return err
	}
	return updateImageLock(ctx, func(lock *image.Lock) error {
		if err := revalidateRetainedImagePolicy(ctx); err != nil {
			return err
		}
		return applyImagePolicyReceipt(lock, request, receipt)
	})
}
