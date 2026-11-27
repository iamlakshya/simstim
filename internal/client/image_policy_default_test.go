package client

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

func TestSavedDefaultAliasCannotRedirectReleaseDefaultOrItsHistory(t *testing.T) {
	// These records prove dispatch and provenance labels only. They are not
	// native roots or evidence that a release image can execute.
	digest := "sha256:" + strings.Repeat("f", 64)
	root := strings.Repeat("d", 64)
	manifest := supply.Manifest{Release: "0.6.0", Items: []supply.Item{{Kind: supply.KindImage, Name: "agent-base", Arch: "amd64", Digest: digest, RootHash: root, DataBlocks: 1}}}
	c, _, ctx := savedImageTransportFixture(t, func(request proto.Request) (any, *proto.Error) {
		if request.Op != "image.status" {
			return nil, unexpectedRuntimeRequest(request)
		}
		var args image.StatusRequest
		if err := json.Unmarshal(request.Args, &args); err != nil {
			t.Error(err)
			return nil, unexpectedRuntimeRequest(request)
		}
		if args.Request == nil || args.Request.SavedHost != nil || args.Request.Digest != digest || args.Request.PlatformDigest != digest || args.Request.RootHash != root {
			t.Error("a saved alias redirected the private release default")
			return nil, unexpectedRuntimeRequest(request)
		}
		return image.CacheStatus{Present: true, Image: &image.Record{Ref: args.Request.Ref, Digest: digest, PlatformDigest: digest, Arch: "amd64", RootHash: root, DataBlocks: 1, Size: 12288}}, nil
	})
	useRuntimeDeliveryManifest(t, manifest)
	saved := savedImageUseResultFixture()
	saved.Image.Ref = "default"
	saved.Image.SavedHost.Ref = "default"
	saved.Image.SavedAliases = []string{"default"}
	writeSavedImagePinFixture(t, ctx, saved)
	got, err := EnsureImage(ctx, c, "default", ImageOptions{Mode: "host", Arch: "amd64"})
	if err != nil || got.Digest != digest || got.SavedHost != nil {
		t.Fatalf("default image was shadowed: %+v %v", got, err)
	}
	selection, err := c.imageSelectionForNeedV1(ctx, "default", "amd64")
	if err != nil || selection == nil || selection.Origin != imageOriginRelease || selection.Release != manifest.Release || selection.Image.Digest != digest || selection.Image.SavedHost != nil {
		t.Fatalf("default history lost its original release producer: %+v %v", selection, err)
	}
}
