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

type signatureTransport func(*http.Request) (*http.Response, error)

func (f signatureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func signatureDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func signatureRegistry(t *testing.T, documents map[string][]byte) (*Registry, *[]string) {
	t.Helper()
	var calls []string
	return &Registry{Client: &http.Client{Transport: signatureTransport(func(request *http.Request) (*http.Response, error) {
		calls = append(calls, request.URL.Path)
		if request.URL.Scheme != "https" || request.URL.Host != "registry.example" {
			t.Fatalf("signature source changed: %s", request.URL)
		}
		data, ok := documents[request.URL.Path]
		status := http.StatusOK
		if !ok {
			status = http.StatusNotFound
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data))}, nil
	})}}, &calls
}

func referringFixture(subject string, bundle []byte) (signatureDescriptor, map[string][]byte) {
	layer := signatureDescriptor{MediaType: SignatureBundleType, Digest: signatureDigest(bundle), Size: int64(len(bundle))}
	manifest, _ := json.Marshal(signatureDocument{SchemaVersion: 2, MediaType: "application/vnd.oci.image.manifest.v1+json", ArtifactType: SignatureBundleType, Subject: signatureDescriptor{Digest: subject}, Layers: []signatureDescriptor{layer}})
	descriptor := signatureDescriptor{MediaType: "application/vnd.oci.image.manifest.v1+json", ArtifactType: SignatureBundleType, Digest: signatureDigest(manifest), Size: int64(len(manifest))}
	return descriptor, map[string][]byte{
		"/v2/team/dev/manifests/" + descriptor.Digest: manifest,
		"/v2/team/dev/blobs/" + layer.Digest:          bundle,
	}
}

func TestImageSignaturesUseReferrersAndTagFallbackWithCheckedBlobs(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	bundle := []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}`)
	for _, location := range []string{"referrers/" + digest, "manifests/sha256-" + digest[7:]} {
		t.Run(location, func(t *testing.T) {
			descriptor, documents := referringFixture(digest, bundle)
			index, _ := json.Marshal(signatureDocument{SchemaVersion: 2, MediaType: "application/vnd.oci.image.index.v1+json", Manifests: []signatureDescriptor{descriptor}})
			documents["/v2/team/dev/"+location] = index
			registry, calls := signatureRegistry(t, documents)
			ref, _ := ParseReference("registry.example/team/dev:1")
			got, err := registry.Signatures(context.Background(), ref, digest)
			if err != nil || len(got) != 1 || !bytes.Equal(got[0].Bundle, bundle) || len(got[0].Payload) != 0 {
				t.Fatalf("signatures=%+v error=%v calls=%v", got, err, *calls)
			}
			for path := range documents {
				if strings.Contains(path, "/blobs/") {
					documents[path] = []byte("tampered")
				}
			}
			if got, err := registry.Signatures(context.Background(), ref, digest); err == nil || len(got) != 0 {
				t.Fatal("unchecked signature blob accepted")
			}
		})
	}
}

func TestReferringSignatureCannotChangeSubjectOrNativeDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	descriptor, documents := referringFixture("sha256:"+strings.Repeat("b", 64), []byte("bundle"))
	index, _ := json.Marshal(signatureDocument{SchemaVersion: 2, MediaType: "application/vnd.oci.image.index.v1+json", Manifests: []signatureDescriptor{descriptor}})
	documents["/v2/team/dev/referrers/"+digest] = index
	registry, _ := signatureRegistry(t, documents)
	ref, _ := ParseReference("registry.example/team/dev:1")
	got, err := registry.Signatures(context.Background(), ref, digest)
	if err == nil || len(got) != 0 {
		t.Fatal("signature on another subject accepted")
	}
}

func TestMissingImageSignatureNeverBecomesAnUnboundedLayerFetch(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	registry, calls := signatureRegistry(t, nil)
	ref, _ := ParseReference("registry.example/team/dev:1")
	got, err := registry.Signatures(context.Background(), ref, digest)
	if err != nil || len(got) != 0 || len(*calls) != 3 {
		t.Fatalf("missing signature: %v %v %v", got, err, *calls)
	}
	documents := map[string][]byte{}
	layers := make([]signatureDescriptor, maxImageSignatures+1)
	for i := range layers {
		layers[i] = signatureDescriptor{Digest: digest, Size: 1, MediaType: "application/vnd.dev.cosign.simplesigning.v1+json"}
	}
	documents["/v2/team/dev/manifests/sha256-"+digest[7:]+".sig"], _ = json.Marshal(signatureDocument{SchemaVersion: 2, MediaType: "application/vnd.oci.image.manifest.v1+json", Layers: layers})
	registry, calls = signatureRegistry(t, documents)
	if _, err := registry.Signatures(context.Background(), ref, digest); err == nil {
		t.Fatal("oversized signature list accepted")
	}
	if len(*calls) != 3 {
		t.Fatal(fmt.Sprintf("oversized list fetched layers: %v", *calls))
	}
}
