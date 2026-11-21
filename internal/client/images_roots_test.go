package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestConcurrentFirstImageRootsCompareUnderClientLock(t *testing.T) {
	isolateRuntimeDelivery(t)
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	registerImageFixtureHosts(t, "first", "second")
	previous := http.DefaultTransport
	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","layers":[]}`)
	http.DefaultTransport = runtimeDeliveryTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	entered := make(chan string, 2)
	firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
	var onceFirst, onceSecond sync.Once
	releaseFirst := func() { onceFirst.Do(func() { close(firstRelease) }) }
	releaseSecond := func() { onceSecond.Do(func() { close(secondRelease) }) }
	defer releaseFirst()
	defer releaseSecond()
	var removed atomic.Bool
	makePeer := func(name, root string, release <-chan struct{}) *Conn {
		connection, _ := newRuntimeDeliveryPeer(t, name, func(request proto.Request) (any, *proto.Error) {
			switch request.Op {
			case "image.status":
				return missingImageStatus(t, request)
			case "image.pull":
				var args image.Request
				if err := json.Unmarshal(request.Args, &args); err != nil {
					t.Error(err)
				}
				if args.RootHash != "" {
					t.Error("fixture did not race two first conversions")
				}
				entered <- name
				<-release
				return image.Record{Ref: args.Ref, Digest: args.Digest, PlatformDigest: args.PlatformDigest, Arch: args.Arch, RootHash: root, DataBlocks: 1, Size: 12288}, nil
			case "image.install":
				return nil, nil
			case "image.rm":
				removed.Store(true)
				return nil, nil
			default:
				return nil, unexpectedRuntimeRequest(request)
			}
		})
		connection.Features = []string{"images"}
		bindImageFixturePeer(t, connection)
		return connection
	}
	first := makePeer("first", strings.Repeat("a", 64), firstRelease)
	second := makePeer("second", strings.Repeat("b", 64), secondRelease)
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	options := ImageOptions{Mode: "host", Arch: "amd64"}
	go func() {
		_, err := PullImage(context.Background(), first, "registry.example/team/dev:1", options)
		firstDone <- err
	}()
	go func() {
		_, err := PullImage(context.Background(), second, "registry.example/team/dev:1", options)
		secondDone <- err
	}()
	for range 2 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("first conversions did not reach the root race")
		}
	}
	releaseFirst()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	releaseSecond()
	err := <-secondDone
	var user *UserError
	if !errors.As(err, &user) || user.Code != "image_root_mismatch" || !removed.Load() {
		t.Fatalf("second first-conversion root was accepted or left on host: %v, removed %v", err, removed.Load())
	}
	if err := image.UpdateLock(context.Background(), ConfigDir(), func(lock *image.Lock) error {
		platform := lock.Images[0].Platforms[0]
		if platform.RootHash != strings.Repeat("a", 64) || platform.RootFrom != "first" {
			t.Fatalf("first root authority was overwritten: %+v", platform)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
