package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func imageManifestSource(t *testing.T) (*string, *int) {
	t.Helper()
	previous := http.DefaultTransport
	version := "one"
	calls := 0
	http.DefaultTransport = runtimeDeliveryTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		data := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","annotations":{"version":%q},"layers":[]}`, version))
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	return &version, &calls
}

func imageReply(t *testing.T, root *string) (*Conn, *runtimeDeliveryPeer) {
	t.Helper()
	connection, peer := newRuntimeDeliveryPeer(t, "box", func(request proto.Request) (any, *proto.Error) {
		switch request.Op {
		case "image.status":
			return missingImageStatus(t, request)
		case "image.pull":
			var args image.Request
			if err := json.Unmarshal(request.Args, &args); err != nil {
				t.Error(err)
			}
			return image.Record{Ref: args.Ref, Digest: args.Digest, PlatformDigest: args.PlatformDigest, Arch: args.Arch, RootHash: *root, DataBlocks: 1, Size: 12288}, nil
		case "image.install":
			var args image.Install
			json.Unmarshal(request.Args, &args)
			if args.Action == "abort" {
				return nil, nil
			}
			return nil, unexpectedRuntimeRequest(request)
		default:
			return nil, unexpectedRuntimeRequest(request)
		}
	})
	connection.Features = []string{"images"}
	return connection, peer
}

func TestImagePullPinsThenRejectsMovedTag(t *testing.T) {
	isolateRuntimeDelivery(t)
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	version, _ := imageManifestSource(t)
	root := strings.Repeat("a", 64)
	connection, _ := imageReply(t, &root)
	var progress bytes.Buffer
	options := ImageOptions{Mode: "host", Arch: "amd64", Progress: &progress}
	first, err := PullImage(context.Background(), connection, "registry.example/team/dev:1", options)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(progress.String(), "Pinned ") {
		t.Fatal("first digest pin was not shown")
	}
	*version = "two"
	_, err = PullImage(context.Background(), connection, "registry.example/team/dev:1", options)
	var user *UserError
	if !errors.As(err, &user) || user.Code != "image_moved" {
		t.Fatalf("moved tag: %v", err)
	}
	options.Update = true
	second, err := PullImage(context.Background(), connection, "registry.example/team/dev:1", options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Digest == first.Digest || !strings.Contains(progress.String(), first.Digest+" -> "+second.Digest) {
		t.Fatal("explicit update did not show both digests")
	}
}

func TestImageEnsureNeverFollowsPinnedTag(t *testing.T) {
	isolateRuntimeDelivery(t)
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	version, _ := imageManifestSource(t)
	root := strings.Repeat("a", 64)
	connection, _ := imageReply(t, &root)
	options := ImageOptions{Mode: "host", Arch: "amd64"}
	first, err := PullImage(context.Background(), connection, "registry.example/team/dev:1", options)
	if err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	http.DefaultTransport = runtimeDeliveryTransport(func(request *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(request.URL.Path, first.Digest) {
			return nil, fmt.Errorf("ensure followed mutable tag: %s", request.URL.Path)
		}
		*version = "one"
		return previous.RoundTrip(request)
	})
	second, err := EnsureImage(context.Background(), connection, "registry.example/team/dev:1", options)
	if err != nil {
		t.Fatal(err)
	}
	if second.Digest != first.Digest {
		t.Fatal("host or mutable tag changed client pin")
	}
	root = strings.Repeat("b", 64)
	_, err = EnsureImage(context.Background(), connection, "registry.example/team/dev:1", options)
	var user *UserError
	if !errors.As(err, &user) || user.Code != "image_root_mismatch" {
		t.Fatalf("wrong root accepted: %v", err)
	}
}

func TestImageHostCannotSupplyDigestAuthority(t *testing.T) {
	isolateRuntimeDelivery(t)
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	imageManifestSource(t)
	connection, _ := newRuntimeDeliveryPeer(t, "box", func(request proto.Request) (any, *proto.Error) {
		if request.Op == "image.status" {
			return missingImageStatus(t, request)
		}
		if request.Op == "image.install" {
			return nil, nil
		}
		var args image.Request
		json.Unmarshal(request.Args, &args)
		return image.Record{Ref: args.Ref, Digest: "sha256:" + strings.Repeat("f", 64), PlatformDigest: args.PlatformDigest, Arch: args.Arch, RootHash: strings.Repeat("a", 64), DataBlocks: 1, Size: 12288}, nil
	})
	connection.Features = []string{"images"}
	_, err := PullImage(context.Background(), connection, "registry.example/team/dev:1", ImageOptions{Mode: "host", Arch: "amd64"})
	var user *UserError
	if !errors.As(err, &user) || user.Code != "image_protocol" {
		t.Fatalf("host chose digest: %v", err)
	}
}

func TestImagePinAndFirstRootAreRecordedOnlyByClient(t *testing.T) {
	isolateRuntimeDelivery(t)
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	imageManifestSource(t)
	root := strings.Repeat("a", 64)
	connection, _ := imageReply(t, &root)
	record, err := PullImage(context.Background(), connection, "registry.example/team/dev:1", ImageOptions{Mode: "host", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if err := image.UpdateLock(context.Background(), ConfigDir(), func(lock *image.Lock) error {
		if len(lock.Images) != 1 || lock.Images[0].Digest != record.Digest || len(lock.Images[0].Platforms) != 1 || lock.Images[0].Platforms[0].RootHash != root || lock.Images[0].Platforms[0].RootFrom != "box" {
			t.Fatalf("wrong client lock %+v", lock)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func imageCopyReply(t *testing.T, hostFails bool, secret string) (*Conn, *runtimeDeliveryPeer) {
	t.Helper()
	var pinned image.Request
	var received int64
	token := strings.Repeat("a", 32)
	connection, peer := newRuntimeDeliveryPeer(t, "box", func(request proto.Request) (any, *proto.Error) {
		if request.Op == "image.status" {
			return missingImageStatus(t, request)
		}
		if secret != "" && strings.Contains(string(request.Args), secret) {
			t.Error("registry login reached the host")
		}
		if request.Op == "image.pull" {
			if !hostFails {
				t.Error("private image tried a host download")
			}
			return nil, &proto.Error{Code: "image", Message: "registry unreachable", Hint: "Use laptop delivery."}
		}
		if request.Op != "image.install" {
			return nil, unexpectedRuntimeRequest(request)
		}
		var args image.Install
		if err := json.Unmarshal(request.Args, &args); err != nil {
			t.Error(err)
			return nil, unexpectedRuntimeRequest(request)
		}
		switch args.Action {
		case "begin":
			if args.Request == nil {
				t.Error("begin lacks client authority")
				return nil, unexpectedRuntimeRequest(request)
			}
			pinned = *args.Request
			return image.Progress{Token: token}, nil
		case "chunk":
			received += int64(len(args.Data))
			return image.Progress{Token: token, Received: received}, nil
		case "commit":
			return image.Record{Ref: pinned.Ref, Digest: pinned.Digest, PlatformDigest: pinned.PlatformDigest, Arch: pinned.Arch, RootHash: strings.Repeat("a", 64), DataBlocks: 1, Size: 12288}, nil
		case "abort":
			return nil, nil
		default:
			return nil, unexpectedRuntimeRequest(request)
		}
	})
	connection.Features = []string{"images"}
	return connection, peer
}

func missingImageStatus(t *testing.T, request proto.Request) (any, *proto.Error) {
	t.Helper()
	var args image.StatusRequest
	if err := json.Unmarshal(request.Args, &args); err != nil {
		t.Error(err)
	}
	if args.Token != "" {
		return image.Progress{Token: args.Token}, nil
	}
	return image.CacheStatus{}, nil
}

func TestPrivateImageAlwaysCopiesAndKeepsLoginLocal(t *testing.T) {
	isolateRuntimeDelivery(t)
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	secret := "registry-private-password"
	encoded := base64.StdEncoding.EncodeToString([]byte("registry-user:" + secret))
	data, err := json.Marshal(map[string]any{"auths": map[string]any{"registry.example": map[string]string{"auth": encoded}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	imageManifestSource(t)
	previous := http.DefaultTransport
	authenticated := false
	http.DefaultTransport = runtimeDeliveryTransport(func(request *http.Request) (*http.Response, error) {
		user, password, ok := request.BasicAuth()
		if !ok || user != "registry-user" || password != secret {
			t.Error("laptop registry login was not used")
		}
		authenticated = true
		return previous.RoundTrip(request)
	})
	connection, peer := imageCopyReply(t, false, secret)
	if _, err := PullImage(context.Background(), connection, "registry.example/team/dev:1", ImageOptions{Mode: "host", Arch: "amd64"}); err != nil {
		t.Fatal(err)
	}
	if !authenticated || peer.count("image.pull", "") != 0 || peer.count("image.install", "") < 3 {
		t.Fatal("private image did not stay on the laptop copy path")
	}
}

func TestImageAutoFallsBackToLaptopOnce(t *testing.T) {
	isolateRuntimeDelivery(t)
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	imageManifestSource(t)
	connection, peer := imageCopyReply(t, true, "")
	var progress bytes.Buffer
	if _, err := PullImage(context.Background(), connection, "registry.example/team/dev:1", ImageOptions{Mode: "auto", Arch: "amd64", Progress: &progress}); err != nil {
		t.Fatal(err)
	}
	if peer.count("image.pull", "") != 1 || !strings.Contains(progress.String(), "Copying from this laptop once") {
		t.Fatal("host failure did not take one explicit laptop fallback")
	}
}
