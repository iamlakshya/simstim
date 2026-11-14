package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

// ImageOptions selects image delivery without sending registry credentials.
type ImageOptions struct {
	Mode     string
	Arch     string
	Update   bool
	Progress io.Writer
}

func imageFeature(c *Conn) error {
	if c == nil {
		return fmt.Errorf("image operation needs a host connection")
	}
	for _, feature := range c.Features {
		if feature == "images" {
			return nil
		}
	}
	return &UserError{Msg: "the helper on " + CleanLine(c.Host.Name) + " does not support images", Hint: "Upgrade the host helper.", Code: "feature_missing"}
}

func imageArch(ctx context.Context, c *Conn, arch string) (string, error) {
	if arch == "" {
		probe, err := c.CapProbe(ctx, "")
		if err != nil {
			return "", err
		}
		if probe.OS != "linux" {
			return "", &UserError{Msg: "images need a Linux host", Hint: "Use a Linux host on amd64 or arm64.", Code: "image_platform"}
		}
		arch = probe.Arch
	}
	if arch != "amd64" && arch != "arm64" {
		return "", &UserError{Msg: "unsupported image architecture " + CleanLine(arch), Hint: "Use an amd64 or arm64 Linux host.", Code: "image_platform"}
	}
	return arch, nil
}

func imageClientError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var user *UserError
	if errors.As(err, &user) {
		return err
	}
	var pe *proto.Error
	if errors.As(err, &pe) {
		return &UserError{Msg: CleanLine(pe.Message), Hint: CleanLine(pe.Hint), Code: pe.Code, Kind: HostKind(pe.Code)}
	}
	return &UserError{Msg: "could not prepare the image: " + CleanLine(err.Error()), Hint: "Check the registry login and run host doctor.", Code: "image"}
}

// ListImages returns host inventory, which never chooses the client's pins.
func ListImages(ctx context.Context, c *Conn) ([]image.Record, error) {
	if err := imageFeature(c); err != nil {
		return nil, err
	}
	var records []image.Record
	err := c.Call(ctx, "image.ls", struct{}{}, &records)
	return records, imageClientError(err)
}

// RemoveImage removes one platform digest, refusing trusted session references.
func RemoveImage(ctx context.Context, c *Conn, digest string) error {
	if err := imageFeature(c); err != nil {
		return err
	}
	if !image.ValidDigest(digest) {
		return &UserError{Msg: "image rm needs a platform SHA-256 digest", Hint: "Choose the digest from image ls.", Code: "bad_image_digest", Kind: KindUsage}
	}
	return imageClientError(c.Call(ctx, "image.rm", struct {
		Digest string `json:"digest"`
	}{digest}, nil))
}

// PullImage resolves a reference on the laptop and explicitly refreshes a pin.
func PullImage(ctx context.Context, c *Conn, ref string, options ImageOptions) (image.Record, error) {
	return prepareImage(ctx, c, ref, options, true)
}

// EnsureImage uses an existing pin without resolving a mutable registry tag.
func EnsureImage(ctx context.Context, c *Conn, ref string, options ImageOptions) (image.Record, error) {
	options.Update = false
	return prepareImage(ctx, c, ref, options, false)
}

func prepareImage(ctx context.Context, c *Conn, text string, options ImageOptions, resolve bool) (record image.Record, returned error) {
	defer func() { returned = imageClientError(returned) }()
	if err := imageFeature(c); err != nil {
		return image.Record{}, err
	}
	if err := ValidateSettings(LoadSettings()); err != nil {
		return image.Record{}, err
	}
	mode := options.Mode
	var err error
	if mode == "" {
		mode, err = RuntimeFetchPreference(ctx)
	} else {
		mode, err = RuntimeFetchMode(mode)
	}
	if err != nil {
		return image.Record{}, err
	}
	arch, err := imageArch(ctx, c, options.Arch)
	if err != nil {
		return image.Record{}, err
	}
	resolved, err := resolveImage(ctx, text, arch, resolve, options)
	if err != nil {
		return image.Record{}, err
	}
	request := resolved.request
	record, err = deliverImage(ctx, c, resolved, mode, options.Progress)
	if err != nil {
		return image.Record{}, err
	}
	if err := image.CheckRecord(request, record); err != nil {
		return image.Record{}, imageClientError(err)
	}
	if !resolved.release {
		if err := recordImageRoot(ctx, c.Host.Name, request, record); err != nil {
			var user *UserError
			if errors.As(err, &user) && user.Code == "image_root_mismatch" {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
				removeErr := RemoveImage(cleanup, c, request.PlatformDigest)
				cancel()
				if removeErr != nil {
					return image.Record{}, errors.Join(err, removeErr)
				}
			}
			return image.Record{}, err
		}
	}
	runtimeLine(options.Progress, "%s: %s, root %s\n", CleanLine(request.Ref), request.Digest, record.RootHash)
	return record, nil
}

func deliverImage(ctx context.Context, c *Conn, resolved resolvedImage, mode string, progress io.Writer) (record image.Record, err error) {
	var cached image.CacheStatus
	if err := c.Call(ctx, "image.status", image.StatusRequest{Request: &resolved.request}, &cached); err != nil {
		return record, err
	}
	if cached.Present {
		if cached.Image == nil {
			return record, imageCacheProtocolError()
		}
		if err := image.CheckRecord(resolved.request, *cached.Image); err != nil {
			return record, err
		}
		return *cached.Image, nil
	}
	if cached.Image != nil {
		return record, imageCacheProtocolError()
	}
	if resolved.registry == nil {
		resolved.registry, err = ImageRegistry(ctx, resolved.request.Ref)
		if err != nil {
			return record, err
		}
	}
	if resolved.registry.Auth != (image.Credentials{}) {
		mode = "laptop"
	}
	if mode == "auto" || mode == "host" {
		record, err = fetchImageHost(ctx, c, resolved.request, progress)
		if err == nil {
			return record, nil
		}
		if mode == "host" || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return record, err
		}
		runtimeLine(progress, "The host image download failed. Copying from this laptop once.\n")
	}
	resolved, err = loadImageCopyPlan(ctx, resolved)
	if err != nil {
		return record, err
	}
	return copyImage(ctx, c, resolved.request, resolved.registry, resolved.documents, resolved.layers, progress)
}

func imageCacheProtocolError() error {
	return &UserError{Msg: "the host returned an invalid cached-image status", Hint: "Upgrade the helper and pull the image again.", Code: "image_protocol"}
}

func registryPinnedReference(ref image.Reference, digest string) string {
	return ref.Registry + "/" + ref.Repository + "@" + digest
}

func pinImage(ctx context.Context, request *image.Request, options ImageOptions) error {
	return updateImageLock(ctx, func(lock *image.Lock) error {
		index := -1
		for i := range lock.Images {
			if lock.Images[i].Ref == request.Ref {
				index = i
				break
			}
		}
		if index < 0 {
			lock.Images = append(lock.Images, image.Pin{Ref: request.Ref, Digest: request.Digest, PinnedAt: time.Now().UTC()})
			index = len(lock.Images) - 1
			runtimeLine(options.Progress, "Pinned %s to %s\n", CleanLine(request.Ref), request.Digest)
		}
		pin := &lock.Images[index]
		if pin.HostID != "" {
			return &UserError{Msg: "this image belongs to the host that saved it", Hint: "Use that host.", Code: "image_elsewhere", Kind: HostKind("image_elsewhere")}
		}
		if pin.Digest != request.Digest {
			if !options.Update {
				return &UserError{Msg: fmt.Sprintf("%s now points at %s, and images.lock pins %s", CleanLine(request.Ref), request.Digest, pin.Digest), Hint: "Run image pull with --update after checking the new image.", Code: "image_moved", Kind: HostKind("image_moved")}
			}
			runtimeLine(options.Progress, "Updating %s: %s -> %s\n", CleanLine(request.Ref), pin.Digest, request.Digest)
			*pin = image.Pin{Ref: request.Ref, Digest: request.Digest, PinnedAt: time.Now().UTC()}
		}
		for _, platform := range pin.Platforms {
			if platform.Arch == request.Arch {
				if platform.Digest != request.PlatformDigest {
					return &UserError{Msg: "the pinned image index returned another platform digest", Hint: "Check the registry and report this as a bug.", Code: "digest_mismatch"}
				}
				request.RootHash = platform.RootHash
				request.DataBlocks = platform.DataBlocks
				return nil
			}
		}
		pin.Platforms = append(pin.Platforms, image.PlatformPin{Arch: request.Arch, Digest: request.PlatformDigest})
		return nil
	})
}

func recordImageRoot(ctx context.Context, host string, request image.Request, record image.Record) error {
	return updateImageLock(ctx, func(lock *image.Lock) error {
		for i := range lock.Images {
			pin := &lock.Images[i]
			if pin.Ref != request.Ref {
				continue
			}
			if pin.Digest != request.Digest {
				return &UserError{Msg: "the image pin changed during its transfer", Hint: "Pull the image again.", Code: "image_moved", Kind: HostKind("image_moved")}
			}
			for j := range pin.Platforms {
				platform := &pin.Platforms[j]
				if platform.Arch != request.Arch {
					continue
				}
				if platform.Digest != request.PlatformDigest {
					return fmt.Errorf("image platform changed during transfer")
				}
				if platform.RootHash != "" && (platform.RootHash != record.RootHash || platform.DataBlocks != record.DataBlocks) {
					return &UserError{Msg: fmt.Sprintf("image root %s from %s differs from %s from %s", record.RootHash, CleanLine(host), platform.RootHash, CleanLine(platform.RootFrom)), Hint: "Run host doctor and report both root hashes.", Code: "image_root_mismatch"}
				}
				platform.RootHash = record.RootHash
				platform.DataBlocks = record.DataBlocks
				if platform.RootFrom == "" {
					platform.RootFrom = host
				}
				return nil
			}
		}
		return fmt.Errorf("image pin disappeared during transfer")
	})
}

func copyImage(ctx context.Context, c *Conn, request image.Request, registry *image.Registry, documents []image.Document, layers []image.Layer, progress io.Writer) (record image.Record, returned error) {
	var transfer image.Progress
	if err := c.Call(ctx, "image.install", image.Install{Action: "begin", Request: &request}, &transfer); err != nil {
		return record, err
	}
	if !supply.IsHex64(transfer.Token + transfer.Token) {
		return record, fmt.Errorf("invalid image transfer token")
	}
	defer func() {
		if returned != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = c.Call(cleanup, "image.install", image.Install{Action: "abort", Token: transfer.Token}, nil)
		}
	}()
	for _, doc := range documents {
		if err := copyImageBlob(ctx, c, transfer.Token, doc.Digest, strings.NewReader(string(doc.Data))); err != nil {
			return record, err
		}
	}
	ref, _ := image.ParseReference(request.Ref)
	seen := map[string]bool{}
	for _, layer := range layers {
		if seen[layer.Digest] {
			continue
		}
		seen[layer.Digest] = true
		file, err := image.CacheBlob(ctx, filepath.Join(CacheDir(), "images"), registry, ref, layer)
		if err != nil {
			return record, err
		}
		err = copyImageBlob(ctx, c, transfer.Token, layer.Digest, file)
		file.Close()
		if err != nil {
			return record, err
		}
		runtimeLine(progress, "%s: %d bytes, sha256 ok\n", layer.Digest, layer.Size)
	}
	returned = c.Call(ctx, "image.install", image.Install{Action: "commit", Token: transfer.Token}, &record)
	return record, returned
}

func copyImageBlob(ctx context.Context, c *Conn, token, digest string, reader io.Reader) error {
	buffer := make([]byte, image.MaxChunk)
	var offset int64
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			var status image.Progress
			if callErr := c.Call(ctx, "image.install", image.Install{Action: "chunk", Token: token, Digest: digest, Offset: offset, Data: buffer[:n]}, &status); callErr != nil {
				return callErr
			}
			offset += int64(n)
			if status.Token != token || status.Received < offset || status.Received > image.MaxCompressed+2*image.MaxManifest {
				return fmt.Errorf("invalid host image progress")
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
