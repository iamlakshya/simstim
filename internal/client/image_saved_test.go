package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func savedImageTransportFixture(t *testing.T, handle func(proto.Request) (any, *proto.Error)) (*Conn, *runtimeDeliveryPeer, context.Context) {
	t.Helper()
	h := h5GrantState(t)
	c, peer := newRuntimeDeliveryPeer(t, h.Name, handle)
	c.Host, c.hostID = h, strings.Repeat("a", 32)
	c.proc, c.route = &hostStateGeneratedProc{}, route{t: &hostStateGeneratedTransport{}}
	c.Features = []string{proto.FeatureImageSave, "images", "session.id"}
	owned, cleanup, attempt, err := beginHostTransportObservation(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := finishHostTransportObservation(owned, attempt, c); err != nil {
		t.Fatal(err)
	}
	r := NewGrantRequest(false, false)
	ctx := WithGrantRequest(t.Context(), r)
	t.Cleanup(func() { _ = r.Close() })
	return c, peer, ctx
}

// These local pins and replies are syntax fixtures for the USE branch. They
// prove client publication/routing only, never native saved-root authority.
func savedImageUseResultFixture() image.SaveResult {
	digest := "sha256:" + strings.Repeat("b", 64)
	return image.SaveResult{HostID: strings.Repeat("a", 32), Image: image.Record{
		Ref: "a..", Digest: digest, PlatformDigest: digest, Arch: "amd64", RootHash: strings.Repeat("c", 64), DataBlocks: 1, Size: 12288,
		ConvertedAt: time.Unix(1, 0), UsedAt: time.Unix(1, 0), SavedAliases: []string{"a.."},
		SavedHost: &image.SavedHostV1{Version: 1, HostID: strings.Repeat("a", 32), Ref: "a..", Digest: digest}}}
}

func writeSavedImagePinFixture(t *testing.T, ctx context.Context, result image.SaveResult) {
	t.Helper()
	if err := result.Validate(result.Image.Ref, result.HostID); err != nil {
		t.Fatal(err)
	}
	if err := updateImageLock(ctx, func(lock *image.Lock) error {
		record := result.Image
		lock.Images = append(lock.Images, image.Pin{Ref: record.Ref, HostID: result.HostID, Digest: record.Digest, PinnedAt: time.Unix(1, 0),
			Platforms: []image.PlatformPin{{Arch: record.Arch, Digest: record.PlatformDigest, RootHash: record.RootHash, DataBlocks: record.DataBlocks}}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSavedImageRequestNeverSendsAnotherHostsDigest(t *testing.T) {
	c, peer, ctx := savedImageTransportFixture(t, func(request proto.Request) (any, *proto.Error) {
		return nil, unexpectedRuntimeRequest(request)
	})
	result := savedImageUseResultFixture()
	result.HostID = strings.Repeat("e", 32)
	result.Image.SavedHost.HostID = result.HostID
	writeSavedImagePinFixture(t, ctx, result)
	_, err := EnsureImage(ctx, c, "a..", ImageOptions{Mode: "host", Arch: "amd64"})
	var user *UserError
	if !errors.As(err, &user) || user.Code != "image_elsewhere" || len(peer.snapshot()) != 0 {
		t.Fatalf("another host received saved authority or registry fallback: %v %v", err, peer.snapshot())
	}
	if _, err := c.imageRequestForNeedV1(ctx, "a..", "amd64"); err == nil || len(peer.snapshot()) != 0 {
		t.Fatal("protected Need lookup widened a foreign saved pin", err)
	}
}

func TestSavedImageReuseChecksTypedOriginalHostRootWithoutRegistryFallback(t *testing.T) {
	result := savedImageUseResultFixture()
	c, peer, ctx := savedImageTransportFixture(t, func(request proto.Request) (any, *proto.Error) {
		if request.Op != "image.status" {
			return nil, unexpectedRuntimeRequest(request)
		}
		var args image.StatusRequest
		if err := json.Unmarshal(request.Args, &args); err != nil || args.Request == nil || args.Token != "" || args.Request.SavedHost == nil || args.Request.SavedHost.HostID != result.HostID || args.Request.SavedHost.Ref != "a.." || args.Request.SavedHost.Digest != result.Image.Digest {
			t.Errorf("saved cache request lost its typed source: %s %v", request.Args, err)
		}
		return image.CacheStatus{Present: true, Image: &result.Image}, nil
	})
	writeSavedImagePinFixture(t, ctx, result)
	got, err := EnsureImage(ctx, c, "a..", ImageOptions{Mode: "host", Arch: "amd64"})
	if err != nil || got.SavedHost == nil || got.SavedHost.HostID != result.HostID || peer.count("image.status", "") != 1 {
		t.Fatalf("same-host saved reuse: %+v %v %v", got, err, peer.snapshot())
	}
	request, err := c.imageRequestForNeedV1(ctx, "a..", "amd64")
	if err != nil || request.SavedHost == nil || request.SavedHost.HostID != result.HostID || request.RootHash != result.Image.RootHash {
		t.Fatalf("Need lookup lost original typed pin: %+v %v", request, err)
	}
	if len(peer.snapshot()) != 1 {
		t.Fatalf("Need selection fetched or resolved the saved image: %v", peer.snapshot())
	}
}

func TestMissingSavedRootCannotFallBackToRegistryPreparation(t *testing.T) {
	c, peer, ctx := savedImageTransportFixture(t, func(request proto.Request) (any, *proto.Error) {
		if request.Op == "image.status" {
			return image.CacheStatus{}, nil
		}
		return nil, unexpectedRuntimeRequest(request)
	})
	writeSavedImagePinFixture(t, ctx, savedImageUseResultFixture())
	_, err := EnsureImage(ctx, c, "a..", ImageOptions{Mode: "auto", Arch: "amd64"})
	var user *UserError
	if !errors.As(err, &user) || user.Code != "image_missing" || len(peer.snapshot()) != 1 || peer.count("image.status", "") != 1 {
		t.Fatalf("missing saved root widened into preparation: %v %v", err, peer.snapshot())
	}
}

func TestSavedRootReplyCannotRenewRemovedAndReaddedHostSource(t *testing.T) {
	result := savedImageUseResultFixture()
	c, peer, ctx := savedImageTransportFixture(t, func(request proto.Request) (any, *proto.Error) {
		if request.Op != "image.status" {
			return nil, unexpectedRuntimeRequest(request)
		}
		if err := SaveHosts(t.Context(), nil); err != nil {
			t.Error(err)
		}
		if err := SaveHosts(t.Context(), []Host{{Name: "box"}}); err != nil {
			t.Error(err)
		}
		return image.CacheStatus{Present: true, Image: &result.Image}, nil
	})
	writeSavedImagePinFixture(t, ctx, result)
	if _, err := EnsureImage(ctx, c, "a..", ImageOptions{Mode: "host", Arch: "amd64"}); err == nil || len(peer.snapshot()) != 1 {
		t.Fatalf("saved response renewed its accepted Source: %v %v", err, peer.snapshot())
	}
}
