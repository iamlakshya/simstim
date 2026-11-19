package client

import (
	"context"
	"errors"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

// savedImageRequest selects the local alias before any image digest leaves
// the laptop. A saved alias on another host never falls back to a registry.
func savedImageRequest(ctx context.Context, c *Conn, ref, arch string) (image.Request, bool, error) {
	lock, err := readImagePins(ctx)
	if err != nil {
		return image.Request{}, false, err
	}
	known := false
	for _, pin := range lock.Images {
		if pin.Ref == ref && pin.HostID != "" {
			known = true
			break
		}
	}
	if !known {
		return image.Request{}, false, nil
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return image.Request{}, true, err
	}
	owned, release, err := c.beginGrant(ctx, "select saved image", true)
	if err != nil {
		return image.Request{}, true, err
	}
	defer release()
	lock, err = readImagePins(owned)
	if err != nil {
		return image.Request{}, true, err
	}
	request, err := selectSavedImagePin(lock, ref, c.HostID(), arch)
	return request, true, err
}

func selectSavedImagePin(lock image.Lock, ref, hostID, arch string) (image.Request, error) {
	if !ValidHostID(hostID) || !image.ValidSaveRef(ref) {
		return image.Request{}, grantOperationError()
	}
	for _, pin := range lock.Images {
		if pin.Ref != ref || pin.HostID != hostID {
			continue
		}
		for _, platform := range pin.Platforms {
			if platform.Arch != arch {
				continue
			}
			request := image.Request{
				Ref: ref, Digest: pin.Digest, PlatformDigest: platform.Digest, Arch: arch,
				RootHash: platform.RootHash, DataBlocks: platform.DataBlocks,
				SavedHost: &image.SavedHostV1{Version: 1, HostID: hostID, Ref: ref, Digest: pin.Digest},
			}
			return request, request.Validate()
		}
		return image.Request{}, &UserError{Code: "image_platform", Kind: KindFailed,
			Msg: "the saved image does not match this host's architecture", Hint: "Save a new image on this host, or choose another image."}
	}
	return image.Request{}, &UserError{Code: "image_elsewhere", Kind: KindRefused,
		Msg: "this image name was saved on another host", Hint: "Start it on the host that made it, or build a template on this computer."}
}

func ensureSavedImage(ctx context.Context, c *Conn, ref, arch string) (image.Record, bool, error) {
	request, known, err := savedImageRequest(ctx, c, ref, arch)
	if err != nil || !known {
		return image.Record{}, known, err
	}
	if !c.HasFeature(proto.FeatureImageSave) {
		return image.Record{}, true, &UserError{Code: "feature_missing", Kind: KindFailed,
			Msg: "the host helper cannot use saved images", Hint: "Upgrade the helper on the host that made the image."}
	}
	owned, release, err := c.beginGrant(ctx, "use saved image", true)
	if err != nil {
		return image.Record{}, true, err
	}
	var cached image.CacheStatus
	if err := c.callWithSent(owned, "image.status", image.StatusRequest{Request: &request}, &cached, release); err != nil {
		return image.Record{}, true, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return image.Record{}, true, err
	}
	if !cached.Present {
		if cached.Image != nil {
			return image.Record{}, true, imageCacheProtocolError()
		}
		return image.Record{}, true, &UserError{Code: "image_missing", Kind: KindFailed,
			Msg: "the saved image is no longer on its original host", Hint: "Save it again on that host, or choose another image."}
	}
	if cached.Image == nil {
		return image.Record{}, true, imageCacheProtocolError()
	}
	if err := image.CheckRecord(request, *cached.Image); err != nil {
		return image.Record{}, true, err
	}
	return *cached.Image, true, nil
}

func readImagePins(ctx context.Context) (image.Lock, error) {
	lock, err := image.ReadLock(ctx, ConfigDir())
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return image.Lock{}, err
	}
	if err != nil {
		return image.Lock{}, &UserError{Code: "image_lock", Kind: KindFailed,
			Msg: "could not read images.lock: " + CleanLine(err.Error()), Hint: "Check that images.lock is valid and its folder is private and readable."}
	}
	return lock, nil
}

// imageRequestForNeedV1 repeats only the original host-bound pin selection for
// the protected VM Need builder. A saved alias never becomes an OCI lookup.
func (c *Conn) imageRequestForNeedV1(ctx context.Context, ref, arch string) (image.Request, error) {
	request, saved, err := savedImageRequest(ctx, c, ref, arch)
	if err != nil || saved {
		return request.Clone(), err
	}
	resolved, err := resolveImage(ctx, ref, arch, false, ImageOptions{Arch: arch})
	if err != nil {
		return image.Request{}, err
	}
	return resolved.request.Clone(), nil
}
