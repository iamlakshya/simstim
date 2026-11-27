package image

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type imageTransport func(*http.Request) (*http.Response, error)

func (f imageTransport) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
func imageDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func imageDocument(t testing.TB, value any) Document {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return Document{imageDigest(data), data}
}

func imageRegistryFixture(t testing.TB) (*Registry, Request, []Document, []Layer, map[string][]byte) {
	t.Helper()
	body := flatTar(t, flatFixture{Name: "etc/config", Body: "hello\n", Mode: 0o644})
	layer := Layer{Digest: imageDigest(body), MediaType: "application/vnd.oci.image.layer.v1.tar", Size: int64(len(body))}
	doc := imageDocument(t, map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json", "config": map[string]any{}, "layers": []map[string]any{{"digest": layer.Digest, "size": layer.Size, "mediaType": layer.MediaType}}})
	objects := map[string][]byte{doc.Digest: doc.Data, layer.Digest: body, "latest": doc.Data}
	registry := &Registry{Client: &http.Client{Transport: imageTransport(func(request *http.Request) (*http.Response, error) {
		key := request.URL.Path[strings.LastIndexByte(request.URL.Path, '/')+1:]
		data, ok := objects[key]
		if !ok {
			return nil, fmt.Errorf("unexpected registry path %s", request.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}}
	request := Request{Ref: "registry.example/team/dev:latest", Digest: doc.Digest, PlatformDigest: doc.Digest, Arch: "amd64"}
	return registry, request, []Document{doc}, []Layer{layer}, objects
}

func TestRegistryResolvesDigestOnClient(t *testing.T) {
	registry, request, _, layers, _ := imageRegistryFixture(t)
	resolved, _, gotLayers, err := registry.Resolve(context.Background(), request.Ref, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != request || len(gotLayers) != 1 || gotLayers[0] != layers[0] {
		t.Fatalf("resolved %+v layers %+v", resolved, gotLayers)
	}
	wrong := "sha256:" + strings.Repeat("f", 64)
	ref, _ := ParseReference(request.Ref)
	if _, err := registry.Manifest(context.Background(), ref, wrong); err == nil {
		t.Fatal("unexpected registry digest accepted")
	}
}

func TestRegistryRejectsIndexConfusion(t *testing.T) {
	platform := "sha256:" + strings.Repeat("a", 64)
	entry := map[string]any{"digest": platform, "size": 512, "mediaType": "application/vnd.oci.image.manifest.v1+json", "platform": map[string]string{"os": "linux", "architecture": "amd64"}}
	doc := imageDocument(t, map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": []any{entry}})
	if got, err := Platform(doc, "amd64"); err != nil || got != platform {
		t.Fatalf("platform %s %v", got, err)
	}
	if _, err := Platform(doc, "arm64"); err == nil {
		t.Fatal("missing platform accepted")
	}
	ambiguous := imageDocument(t, map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": []any{entry, entry}})
	if _, err := Platform(ambiguous, "amd64"); err == nil {
		t.Fatal("ambiguous platform accepted")
	}
	doc.Data = append(doc.Data, ' ')
	assertFlatCode(t, func() error { _, err := Platform(doc, "amd64"); return err }(), "digest_mismatch")
}

func TestRegistryBlobSizeAndHash(t *testing.T) {
	registry, request, _, layers, objects := imageRegistryFixture(t)
	ref, _ := ParseReference(request.Ref)
	var output bytes.Buffer
	if err := registry.Blob(context.Background(), ref, layers[0], &output, nil); err != nil {
		t.Fatal(err)
	}
	objects[layers[0].Digest] = append(objects[layers[0].Digest], 'x')
	assertFlatCode(t, registry.Blob(context.Background(), ref, layers[0], io.Discard, nil), "digest_mismatch")
	objects[layers[0].Digest] = bytes.Repeat([]byte("x"), int(layers[0].Size))
	assertFlatCode(t, registry.Blob(context.Background(), ref, layers[0], io.Discard, nil), "digest_mismatch")
}

func TestRegistryManifestAndLayerCaps(t *testing.T) {
	registry, request, _, _, objects := imageRegistryFixture(t)
	objects["latest"] = bytes.Repeat([]byte("x"), int(MaxManifest)+1)
	ref, _ := ParseReference(request.Ref)
	_, err := registry.Manifest(context.Background(), ref, "latest")
	assertFlatCode(t, err, "image_bad_layer")
	for _, count := range []int{MaxLayers + 1} {
		layers := make([]descriptor, count)
		for i := range layers {
			layers[i] = descriptor{Digest: request.Digest, Size: 1}
		}
		doc := imageDocument(t, manifest{SchemaVersion: 2, MediaType: "application/vnd.oci.image.manifest.v1+json", Layers: layers})
		_, err := Layers(doc)
		assertFlatCode(t, err, "image_too_large")
	}
	doc := imageDocument(t, manifest{SchemaVersion: 2, MediaType: "application/vnd.oci.image.manifest.v1+json", Layers: []descriptor{{Digest: request.Digest, Size: MaxCompressed + 1}}})
	_, err = Layers(doc)
	assertFlatCode(t, err, "image_too_large")
}
