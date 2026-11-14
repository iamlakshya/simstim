package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/runtimecache"
	"github.com/iamlakshya/simstim/internal/supply"
)

func isolateRuntimeDelivery(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		dir := filepath.Join(root, key)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
	return root
}

func runtimeDeliveryManifest(t *testing.T, body []byte) supply.Manifest {
	t.Helper()
	release := "0.6.0"
	item := supply.NewItem(supply.KindUserns, "namespace-launcher", "1.0.0", "amd64", body, supply.ReleaseFolder(release)+"/namespace-amd64")
	manifest := supply.Manifest{Release: release, Runtime: "2026.11.0", Items: []supply.Item{item}}
	if err := runtimecache.Validate(manifest, "amd64"); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func useRuntimeDeliveryManifest(t *testing.T, manifest supply.Manifest) {
	t.Helper()
	releaseManifest.Lock()
	previous := releaseManifest.m
	copyManifest := manifest
	copyManifest.Items = append([]supply.Item(nil), manifest.Items...)
	releaseManifest.m = &copyManifest
	releaseManifest.Unlock()
	t.Cleanup(func() { releaseManifest.Lock(); releaseManifest.m = previous; releaseManifest.Unlock() })
}

func runtimeDeliveryProbe(reachable bool) *api.CapProbe {
	return &api.CapProbe{OS: "linux", Arch: "amd64", ReleaseReachable: &reachable}
}

func runtimeDeliveryStatus(manifest supply.Manifest, present bool) api.RuntimeStatus {
	status := api.RuntimeStatus{Runtime: manifest.Runtime, Items: []api.RuntimeFile{}, Drivers: []api.RuntimeDriver{}}
	for _, item := range manifest.Items {
		status.Items = append(status.Items, api.RuntimeFile{Kind: item.Kind, Name: item.Name, SHA256: item.SHA256, Size: item.Size, Present: present})
	}
	return status
}

type runtimeDeliveryPeer struct {
	mu       sync.Mutex
	requests []proto.Request
	handle   func(proto.Request) (any, *proto.Error)
}

func newRuntimeDeliveryPeer(t *testing.T, name string, handle func(proto.Request) (any, *proto.Error)) (*Conn, *runtimeDeliveryPeer) {
	t.Helper()
	reader, writer := io.Pipe()
	buffered := bufio.NewReader(reader)
	conn := &Conn{Host: Host{Name: name}, Features: []string{"sandbox"}, w: proto.NewWriter(writer), pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
	peer := &runtimeDeliveryPeer{handle: handle}
	stopped := make(chan struct{})
	var requests sync.WaitGroup
	go func() {
		defer close(stopped)
		for {
			frame, err := proto.Read(buffered)
			if err != nil {
				return
			}
			var request proto.Request
			if frame.Type != proto.TRequest || json.Unmarshal(frame.Payload, &request) != nil {
				t.Errorf("unexpected runtime frame %v", frame.Type)
				return
			}
			peer.mu.Lock()
			peer.requests = append(peer.requests, request)
			peer.mu.Unlock()
			requests.Add(1)
			go func() {
				defer requests.Done()
				result, requestError := peer.handle(request)
				response := proto.Response{ID: request.ID, OK: requestError == nil, Error: requestError}
				if result != nil {
					encoded, err := json.Marshal(result)
					if err != nil {
						t.Errorf("encode runtime peer response: %v", err)
						return
					}
					response.Result = encoded
				}
				conn.mu.Lock()
				pending := conn.pending[request.ID]
				delete(conn.pending, request.ID)
				conn.mu.Unlock()
				if pending != nil {
					pending <- response
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = writer.Close()
		_ = reader.Close()
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			t.Error("runtime peer did not stop")
		}
		requests.Wait()
	})
	return conn, peer
}

func (p *runtimeDeliveryPeer) snapshot() []proto.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]proto.Request(nil), p.requests...)
}

func (p *runtimeDeliveryPeer) count(op, action string) int {
	count := 0
	for _, request := range p.snapshot() {
		if request.Op != op {
			continue
		}
		if action == "" {
			count++
			continue
		}
		var install runtimecache.RuntimeInstall
		if json.Unmarshal(request.Args, &install) == nil && install.Action == action {
			count++
		}
	}
	return count
}

func unexpectedRuntimeRequest(request proto.Request) *proto.Error {
	return &proto.Error{Code: "test_unexpected", Message: "unexpected request " + request.Op, Hint: "Check the test peer."}
}

type runtimeDeliveryTransport func(*http.Request) (*http.Response, error)

func (f runtimeDeliveryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func runtimeDeliveryHTTP(t *testing.T, item supply.Item, body []byte) *int {
	t.Helper()
	previous := http.DefaultTransport
	calls := 0
	http.DefaultTransport = runtimeDeliveryTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.String() != item.URL {
			return nil, fmt.Errorf("runtime requested unpinned URL %q", request.URL.String())
		}
		if request.Method != http.MethodGet || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			return nil, fmt.Errorf("runtime request has unexpected method or credentials")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Header: make(http.Header), Request: request}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	return &calls
}

func warmRuntimeDeliveryCache(t *testing.T, item supply.Item, body []byte) {
	t.Helper()
	cache, err := runtimecache.Open(filepath.Join(CacheDir(), "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	part, err := cache.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	defer part.Close()
	for offset := 0; offset < len(body); {
		end := min(offset+runtimecache.ChunkSize, len(body))
		if err := part.WriteAt(body[offset:end], int64(offset)); err != nil {
			t.Fatal(err)
		}
		offset = end
	}
	if err := part.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
}
