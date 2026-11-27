package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

func policyValues() *api.ImagePolicyValues {
	identity, issuer := "release@example.com", "https://issuer.example"
	return &api.ImagePolicyValues{Identity: &identity, Issuer: &issuer}
}

func policyRequest(arch string) image.Request {
	return image.Request{Ref: "registry.example/team/dev:1", Digest: "sha256:" + strings.Repeat("a", 64), PlatformDigest: "sha256:" + strings.Repeat("b", 64), Arch: arch}
}

func missingPolicyRegistry(calls *int) *image.Registry {
	return &image.Registry{Client: &http.Client{Transport: runtimeDeliveryTransport(func(*http.Request) (*http.Response, error) {
		*calls++
		return &http.Response{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})}}
}

func TestImagePolicyIndexAndPlatformReceiptsHaveDistinctCoverage(t *testing.T) {
	isolateRuntimeDelivery(t)
	values := policyValues()
	policy, err := readImageCertificatePolicy(context.Background(), values)
	if err != nil {
		t.Fatal(err)
	}
	request := policyRequest("amd64")
	pin := image.Pin{Ref: request.Ref, Digest: request.Digest, PinnedAt: time.Now().UTC(), Platforms: []image.PlatformPin{{Arch: "amd64", Digest: request.PlatformDigest, SignedBy: policy.identity, Policy: policy.hash}}}
	if err := updateImageLock(context.Background(), func(lock *image.Lock) error { lock.Images = []image.Pin{pin}; return nil }); err != nil {
		t.Fatal(err)
	}
	calls := 0
	registry := missingPolicyRegistry(&calls)
	receipt, err := verifyImagePolicy(context.Background(), request, registry, values)
	if err != nil || receipt.digest != request.PlatformDigest || calls != 0 {
		t.Fatalf("selected platform: %+v %v calls=%d", receipt, err, calls)
	}
	other := policyRequest("arm64")
	_, err = verifyImagePolicy(context.Background(), other, registry, values)
	var refused *UserError
	if !errors.As(err, &refused) || refused.Code != "image_signature" || calls == 0 {
		t.Fatalf("another platform used its sibling's signature: %v", err)
	}
	if err := updateImageLock(context.Background(), func(lock *image.Lock) error {
		lock.Images[0].SignedBy = policy.identity
		lock.Images[0].Policy = policy.hash
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if receipt, err := verifyImagePolicy(context.Background(), other, registry, values); err != nil || receipt.digest != request.Digest || calls != 0 {
		t.Fatalf("index coverage: %+v %v", receipt, err)
	}
}

func TestChangedPolicyOrRootNeverReusesOldVerification(t *testing.T) {
	isolateRuntimeDelivery(t)
	values := policyValues()
	policy, err := readImageCertificatePolicy(context.Background(), values)
	if err != nil {
		t.Fatal(err)
	}
	request := policyRequest("amd64")
	if err := updateImageLock(context.Background(), func(lock *image.Lock) error {
		lock.Images = []image.Pin{{Ref: request.Ref, Digest: request.Digest, PinnedAt: time.Now().UTC(), SignedBy: policy.identity, Policy: policy.hash}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	changed := "https://other.example"
	values.Issuer = &changed
	calls := 0
	if _, err := verifyImagePolicy(context.Background(), request, missingPolicyRegistry(&calls), values); err == nil || calls == 0 {
		t.Fatal("changed issuer reused an old verification")
	}
	if policy.hash == supply.ImagePolicyHash(policy.identity, policy.issuer, "", append(policy.root, ' ')) {
		t.Fatal("changed trusted root bytes kept the same policy hash")
	}
	rootPath := filepath.Join(t.TempDir(), "trusted-root.json")
	if err := os.WriteFile(rootPath, policy.root, 0o600); err != nil {
		t.Fatal(err)
	}
	values = policyValues()
	values.TrustedRoot = &rootPath
	calls = 0
	if _, err := verifyImagePolicy(context.Background(), request, missingPolicyRegistry(&calls), values); err == nil || calls == 0 {
		t.Fatalf("changed root selection reused an old verification: %v", err)
	}
	custom, err := readImageCertificatePolicy(context.Background(), values)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateImageLock(context.Background(), func(lock *image.Lock) error { lock.Images[0].Policy = custom.hash; return nil }); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if _, err := verifyImagePolicy(context.Background(), request, missingPolicyRegistry(&calls), values); err != nil || calls != 0 {
		t.Fatalf("unchanged custom root did not reuse its verification: %v", err)
	}
	if err := os.WriteFile(rootPath, append(policy.root, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if _, err := verifyImagePolicy(context.Background(), request, missingPolicyRegistry(&calls), values); err == nil || calls == 0 {
		t.Fatal("replaced trusted-root contents reused the old verification")
	}
}

func TestFailedPolicyDoesNotCreateOrMoveAPin(t *testing.T) {
	connection, _, ctx := savedImageTransportFixture(t, func(request proto.Request) (any, *proto.Error) { return nil, unexpectedRuntimeRequest(request) })
	ctx, _, closePolicy, err := withRetainedImagePolicy(ctx, connection)
	if err != nil {
		t.Fatal(err)
	}
	defer closePolicy()
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	version, _ := imageManifestSource(t)
	request := policyRequest("amd64")
	if _, err := resolveImage(ctx, request.Ref, "amd64", true, ImageOptions{}, policyValues()); err == nil {
		t.Fatal("unsigned image was pinned")
	}
	lock, err := image.ReadLock(ctx, ConfigDir())
	if err != nil || len(lock.Images) != 0 {
		t.Fatalf("failed check changed pins: %+v %v", lock, err)
	}
	*version = "old"
	resolved, err := resolveImage(ctx, request.Ref, "amd64", true, ImageOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	*version = "new"
	_, err = resolveImage(ctx, request.Ref, "amd64", true, ImageOptions{}, policyValues())
	var moved *UserError
	if !errors.As(err, &moved) || moved.Code != "image_moved" {
		t.Fatalf("moving tag bypassed its explicit update gate: %v", err)
	}
	_, err = resolveImage(ctx, request.Ref, "amd64", true, ImageOptions{Update: true}, policyValues())
	if err == nil {
		t.Fatal("explicit update accepted an unsigned replacement")
	}
	lock, err = image.ReadLock(ctx, ConfigDir())
	if err != nil || len(lock.Images) != 1 || lock.Images[0].Digest != resolved.request.Digest {
		t.Fatal("failed replacement changed the existing pin")
	}
}

func TestSavedHostMetadataCannotEnterOCIImagePolicy(t *testing.T) {
	isolateRuntimeDelivery(t)
	request := policyRequest("amd64")
	request.Ref = "saved"
	request.Digest = request.PlatformDigest
	request.RootHash = strings.Repeat("c", 64)
	request.DataBlocks = 1
	request.SavedHost = &image.SavedHostV1{Version: 1, HostID: strings.Repeat("d", 32), Ref: request.Ref, Digest: request.Digest}
	if _, err := verifyImagePolicy(context.Background(), request, nil, policyValues()); err == nil {
		t.Fatal("saved-host description became an OCI signature source")
	}
}
