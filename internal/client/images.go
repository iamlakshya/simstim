package client

import (
	"context"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/authn"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/image"
)

// ImagesLockPath is the laptop-side image pin file (~/.config/<name>/images.lock).
func ImagesLockPath() string { return filepath.Join(ConfigDir(), "images.lock") }

// imageResolveFunc resolves a non-default reference on the laptop. Tests may
// replace it with an offline stub.
var imageResolveFunc = func(ctx context.Context, ref string) (*image.Resolved, error) {
	return image.Resolve(ctx, ref, image.PullOptions{Keychain: authn.DefaultKeychain})
}

func (c *Conn) needImages() error {
	if c.HasFeature("images") {
		return nil
	}
	return &UserError{
		Msg:  "the helper on " + c.Host.Name + " is too old for session images",
		Hint: brand.Name + " host add " + c.Host.Name + " (updates the helper)",
		Code: "wall_helper_old",
	}
}

// Images lists the converted session images stored on the host.
func (c *Conn) Images(ctx context.Context) ([]api.ImageRecord, error) {
	if err := c.needImages(); err != nil {
		return nil, err
	}
	var out []api.ImageRecord
	err := c.Call(ctx, "image.ls", nil, &out)
	return out, err
}

// PullImage asks the host to pull and convert a pinned OCI image by digest.
func (c *Conn) PullImage(ctx context.Context, args api.ImagePullArgs) (api.ImageRecord, error) {
	if err := c.needImages(); err != nil {
		return api.ImageRecord{}, err
	}
	var out api.ImageRecord
	err := c.Call(ctx, "image.pull", args, &out)
	return out, err
}

// RemoveImage deletes one stored session image from the host.
func (c *Conn) RemoveImage(ctx context.Context, digest string) (api.ImageRecord, error) {
	if err := c.needImages(); err != nil {
		return api.ImageRecord{}, err
	}
	var out api.ImageRecord
	err := c.Call(ctx, "image.rm", api.ImageRemoveArgs{Digest: digest}, &out)
	return out, err
}

// BuildAgentsDisk asks the host to rebuild its read-only agents EROFS disk.
func (c *Conn) BuildAgentsDisk(ctx context.Context) (api.AgentsDiskRecord, error) {
	if err := c.needImages(); err != nil {
		return api.AgentsDiskRecord{}, err
	}
	var out api.AgentsDiskRecord
	err := c.Call(ctx, "image.agents", nil, &out)
	return out, err
}

// ImagePinResult describes the resolved, pinned reference ready for PullImage.
type ImagePinResult struct {
	Ref            string
	Digest         string
	IsDefault      bool
	NewlyPinned    bool
	PreviousDigest string
	Entry          image.LockEntry
}

// ResolveAndPinImage resolves ref on the laptop and pins it in images.lock.
// "default" resolves to the pinned official buildpack-deps:trixie-scm image
// without writing images.lock.
func ResolveAndPinImage(ctx context.Context, ref, hostName string, update bool) (ImagePinResult, error) {
	canonical, pinned, isDefault := image.ResolveDefault(ref)
	if isDefault {
		return ImagePinResult{Ref: canonical, Digest: pinned, IsDefault: true}, nil
	}

	resolved, err := imageResolveFunc(ctx, canonical)
	if err != nil {
		return ImagePinResult{}, clientImageError(err)
	}
	entry, created, prev, err := image.Pin(ImagesLockPath(), resolved.Ref, resolved.Digest, hostName, update)
	if err != nil {
		return ImagePinResult{}, clientImageError(err)
	}
	return ImagePinResult{
		Ref:            resolved.Ref,
		Digest:         resolved.Digest,
		NewlyPinned:    created,
		PreviousDigest: prev,
		Entry:          entry,
	}, nil
}

// RecordImageConversion records a host's conversion root hash in images.lock,
// or returns image_root_mismatch if it differs from an earlier host.
func RecordImageConversion(pin ImagePinResult, hostName string, rec api.ImageRecord) error {
	if pin.IsDefault {
		return nil
	}
	err := image.RecordPlatform(ImagesLockPath(), pin.Ref, pin.Digest, image.LockPlatform{
		Arch:       rec.Arch,
		Digest:     rec.PlatformDigest,
		RootHash:   rec.RootHash,
		DataBlocks: rec.DataBlocks,
		RootFrom:   hostName,
	})
	return clientImageError(err)
}

func clientImageError(err error) error {
	if err == nil {
		return nil
	}
	if ie, ok := image.AsError(err); ok {
		return &UserError{
			Msg:  ie.Message,
			Hint: ie.Hint,
			Code: ie.Code,
			Kind: HostKind(ie.Code),
		}
	}
	return err
}
