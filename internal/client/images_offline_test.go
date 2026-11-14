package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func offlineImagePin(t *testing.T) image.Record {
	t.Helper()
	record := image.Record{Ref: "registry.example/team/dev:1", Digest: "sha256:" + strings.Repeat("a", 64), PlatformDigest: "sha256:" + strings.Repeat("b", 64), Arch: "amd64", RootHash: strings.Repeat("c", 64), DataBlocks: 1, Size: 12288}
	if err := image.UpdateLock(context.Background(), ConfigDir(), func(lock *image.Lock) error {
		lock.Images = append(lock.Images, image.Pin{Ref: record.Ref, Digest: record.Digest, PinnedAt: time.Now().UTC(), Platforms: []image.PlatformPin{{Arch: record.Arch, Digest: record.PlatformDigest, RootHash: record.RootHash, DataBlocks: record.DataBlocks, RootFrom: "box"}}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestCachedImageEnsureNeedsNeitherRegistryNorLoginHelper(t *testing.T) {
	isolateRuntimeDelivery(t)
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("invalid login config"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := offlineImagePin(t)
	previous := http.DefaultTransport
	http.DefaultTransport = runtimeDeliveryTransport(func(*http.Request) (*http.Response, error) {
		t.Error("cached ensure contacted the registry")
		return nil, fmt.Errorf("offline")
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	connection, peer := newRuntimeDeliveryPeer(t, "box", func(request proto.Request) (any, *proto.Error) {
		if request.Op != "image.status" {
			return nil, unexpectedRuntimeRequest(request)
		}
		var args image.StatusRequest
		if err := json.Unmarshal(request.Args, &args); err != nil {
			t.Error(err)
		}
		if args.Request == nil || args.Request.Digest != record.Digest || args.Request.PlatformDigest != record.PlatformDigest || args.Request.RootHash != record.RootHash {
			t.Error("cached lookup did not use laptop pins")
		}
		return image.CacheStatus{Present: true, Image: &record}, nil
	})
	connection.Features = []string{"images"}
	got, err := EnsureImage(context.Background(), connection, record.Ref, ImageOptions{Mode: "laptop", Arch: record.Arch})
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != record.Digest || peer.count("image.pull", "") != 0 || peer.count("image.install", "") != 0 {
		t.Fatal("cached image used a delivery path")
	}
}

func TestKnownImageHostDeliveryWorksWhileLaptopRegistryIsOffline(t *testing.T) {
	isolateRuntimeDelivery(t)
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	record := offlineImagePin(t)
	previous := http.DefaultTransport
	http.DefaultTransport = runtimeDeliveryTransport(func(*http.Request) (*http.Response, error) {
		t.Error("known host delivery fetched a laptop manifest")
		return nil, fmt.Errorf("offline")
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	root := record.RootHash
	connection, peer := imageReply(t, &root)
	got, err := EnsureImage(context.Background(), connection, record.Ref, ImageOptions{Mode: "host", Arch: record.Arch})
	if err != nil {
		t.Fatal(err)
	}
	if got.PlatformDigest != record.PlatformDigest || peer.count("image.pull", "") != 1 {
		t.Fatal("known pinned host download was not used")
	}
}

func TestLaptopImageDeliveryNeverUsesHostPullAsCacheProbe(t *testing.T) {
	isolateRuntimeDelivery(t)
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	record := offlineImagePin(t)
	previous := http.DefaultTransport
	http.DefaultTransport = runtimeDeliveryTransport(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("offline") })
	t.Cleanup(func() { http.DefaultTransport = previous })
	root := record.RootHash
	connection, peer := imageReply(t, &root)
	if _, err := EnsureImage(context.Background(), connection, record.Ref, ImageOptions{Mode: "laptop", Arch: record.Arch}); err == nil {
		t.Fatal("missing laptop bytes succeeded offline")
	}
	if peer.count("image.pull", "") != 0 {
		t.Fatal("laptop mode attempted a host registry download")
	}
}
