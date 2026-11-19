package client

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/supply"
)

type resolvedImage struct {
	request   image.Request
	registry  *image.Registry
	documents []image.Document
	layers    []image.Layer
	release   bool
}

func resolveImage(ctx context.Context, text, arch string, resolve bool, options ImageOptions) (resolvedImage, error) {
	var result resolvedImage
	if text == "default" {
		manifest, err := ReleaseManifest()
		if err != nil {
			return result, err
		}
		item, ok := manifest.Find(supply.KindImage, "agent-base", arch)
		if !ok {
			return result, &UserError{Msg: "this release carries no default image pin for " + arch, Hint: "Install a release with the default image, or pull an OCI reference.", Code: "image_missing"}
		}
		text = brand.ImageRepository + "@" + item.Digest
		result.release = true
		result.request = image.Request{Ref: text, Digest: item.Digest, PlatformDigest: item.Digest, Arch: arch, RootHash: item.RootHash, DataBlocks: item.DataBlocks}
		return result, result.request.Validate()
	}
	parsed, err := image.ParseReference(text)
	if err != nil {
		return result, &UserError{Msg: CleanLine(err.Error()), Hint: "Use an OCI repository with a tag or a SHA-256 digest.", Code: "bad_image_ref", Kind: KindUsage}
	}
	lookup := text
	if !resolve {
		request, known, err := knownImageRequest(ctx, text, arch)
		if err != nil {
			return result, err
		}
		if known {
			result.request = request
			return result, nil
		}
		lookup, err = pinnedImageReference(ctx, text, parsed)
		if err != nil {
			return result, err
		}
	}
	registry, err := ImageRegistry(ctx, text)
	if err != nil {
		return result, err
	}
	request, documents, layers, err := registry.Resolve(ctx, lookup, arch)
	if err != nil {
		return result, err
	}
	request.Ref = text
	if err := pinImage(ctx, &request, options); err != nil {
		return result, err
	}
	result.request = request
	result.registry = registry
	result.documents = documents
	result.layers = layers
	return result, nil
}

func knownImageRequest(ctx context.Context, text, arch string) (image.Request, bool, error) {
	var request image.Request
	known := false
	err := updateImageLock(ctx, func(lock *image.Lock) error {
		for _, pin := range lock.Images {
			if pin.Ref != text || pin.HostID != "" {
				continue
			}
			for _, platform := range pin.Platforms {
				if platform.Arch == arch {
					request = image.Request{Ref: text, Digest: pin.Digest, PlatformDigest: platform.Digest, Arch: arch, RootHash: platform.RootHash, DataBlocks: platform.DataBlocks}
					known = true
					return request.Validate()
				}
			}
		}
		return nil
	})
	return request, known, err
}

func loadImageCopyPlan(ctx context.Context, resolved resolvedImage) (resolvedImage, error) {
	if len(resolved.documents) > 0 {
		return resolved, nil
	}
	parsed, err := image.ParseReference(resolved.request.Ref)
	if err != nil {
		return resolved, err
	}
	request, documents, layers, err := resolved.registry.Resolve(ctx, registryPinnedReference(parsed, resolved.request.Digest), resolved.request.Arch)
	if err != nil {
		return resolved, err
	}
	if request.Digest != resolved.request.Digest || request.PlatformDigest != resolved.request.PlatformDigest || request.Arch != resolved.request.Arch {
		return resolved, &UserError{Msg: "the pinned image returned another platform", Hint: "Check the registry and report both platform digests.", Code: "digest_mismatch"}
	}
	resolved.documents = documents
	resolved.layers = layers
	return resolved, nil
}

func pinnedImageReference(ctx context.Context, text string, parsed image.Reference) (string, error) {
	lookup := text
	err := updateImageLock(ctx, func(lock *image.Lock) error {
		for _, pin := range lock.Images {
			if pin.Ref != text || pin.HostID != "" {
				continue
			}
			lookup = registryPinnedReference(parsed, pin.Digest)
			break
		}
		return nil
	})
	return lookup, err
}

func updateImageLock(ctx context.Context, change func(*image.Lock) error) error {
	err := image.UpdateLock(ctx, ConfigDir(), change)
	if err == nil {
		return nil
	}
	var user *UserError
	if errors.As(err, &user) {
		return err
	}
	return &UserError{Msg: "could not update " + filepath.Join(ConfigDir(), "images.lock") + ": " + CleanLine(err.Error()), Hint: "Check that images.lock is valid and its folder is private and writable.", Code: "image_lock"}
}
