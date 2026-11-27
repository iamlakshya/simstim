package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func originalImageHistory() *sandboxImageSelectionV1 {
	request := policyRequest("amd64")
	request.RootHash = strings.Repeat("c", 64)
	request.DataBlocks = 1
	return &sandboxImageSelectionV1{Version: 1, Origin: imageOriginOCI, Image: request}
}

func TestImageHistoryRetainsClosedOriginalOCIAndSavedDiscriminators(t *testing.T) {
	selection := originalImageHistory()
	wire, err := encodeSandboxImageSelection(selection)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSandboxImageSelection(wire)
	if err != nil || !sameSandboxImageSelection(selection, decoded) {
		t.Fatalf("history: %+v %v", decoded, err)
	}
	for _, invalid := range [][]byte{
		bytes.Replace(wire, []byte(`"version":1`), []byte(`"version":2`), 1),
		bytes.Replace(wire, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		bytes.Replace(wire, []byte(`"origin":"oci"`), []byte(`"origin":"saved-host"`), 1),
		bytes.Replace(wire, []byte(`"image":{`), []byte(`"image":{"unknown":1,`), 1),
		append(bytes.Clone(wire), []byte(" {}")...),
	} {
		if _, err := decodeSandboxImageSelection(invalid); err == nil {
			t.Fatal("ambiguous or future history accepted")
		}
	}
	result := savedImageUseResultFixture()
	selection.Origin = imageOriginSaved
	selection.Image = image.Request{Ref: result.Image.Ref, Digest: result.Image.Digest, PlatformDigest: result.Image.PlatformDigest, Arch: result.Image.Arch, RootHash: result.Image.RootHash, DataBlocks: result.Image.DataBlocks, SavedHost: image.CloneSavedHostV1(result.Image.SavedHost)}
	wire, err = encodeSandboxImageSelection(selection)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = decodeSandboxImageSelection(wire)
	if err != nil {
		t.Fatal(err)
	}
	decoded.Image.SavedHost.HostID = strings.Repeat("e", 32)
	if decoded.Image.SavedHost.HostID == selection.Image.SavedHost.HostID {
		t.Fatal("decoded history aliases original saved identity")
	}
	selection.Origin = imageOriginOCI
	if _, err := encodeSandboxImageSelection(selection); err == nil {
		t.Fatal("saved host identity entered OCI history")
	}
}

func TestHistoricalImagePolicyNeverResolvesAChangedTagOrUsesItsNewPin(t *testing.T) {
	c, peer, ctx := savedImageTransportFixture(t, func(request proto.Request) (any, *proto.Error) { return nil, unexpectedRuntimeRequest(request) })
	ctx, _, closePolicy, err := withRetainedImagePolicy(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer closePolicy()
	selection := originalImageHistory()
	current := "sha256:" + strings.Repeat("e", 64)
	if err := updateImageLock(ctx, func(lock *image.Lock) error {
		lock.Images = []image.Pin{{Ref: selection.Image.Ref, Digest: current, PinnedAt: time.Now().UTC()}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	previous := http.DefaultTransport
	calls := 0
	http.DefaultTransport = runtimeDeliveryTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		path := request.URL.Path
		if strings.HasSuffix(path, "/1") || strings.Contains(path, current) || strings.Contains(path, current[7:]) {
			return nil, fmt.Errorf("historical policy followed a current tag or pin: %s", path)
		}
		if !strings.Contains(path, selection.Image.Digest[7:]) && !strings.Contains(path, selection.Image.PlatformDigest[7:]) {
			return nil, fmt.Errorf("unexpected historical registry source: %s", path)
		}
		return &http.Response{StatusCode: 404, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = previous })
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	if _, err := verifyHistoricalImagePolicy(ctx, c, selection, policyValues()); err == nil || calls == 0 {
		t.Fatal("missing original signature was not checked")
	}
	if len(peer.snapshot()) != 0 {
		t.Fatal("historical policy failure sent host work")
	}
	lock, err := image.ReadLock(ctx, ConfigDir())
	if err != nil || len(lock.Images) != 1 || lock.Images[0].Digest != current {
		t.Fatal("historical verification changed the user's current pin")
	}
}

func TestHistoricalImageNeedsOriginalLocalSelectionWhenPolicyIsActive(t *testing.T) {
	if _, err := verifyHistoricalImagePolicy(t.Context(), nil, nil, policyValues()); err == nil {
		t.Fatal("signature policy accepted a start without original local image history")
	}
	selection := originalImageHistory()
	selection.Image.Transfer = strings.Repeat("a", 32)
	if _, err := encodeSandboxImageSelection(selection); err == nil {
		t.Fatal("operation token entered historical image data")
	}
	var fields map[string]any
	wire, _ := json.Marshal(originalImageHistory())
	json.Unmarshal(wire, &fields)
	fields["release"] = "0.1"
	wire, _ = json.Marshal(fields)
	if _, err := decodeSandboxImageSelection(wire); err == nil {
		t.Fatal("OCI history accepted a release-default exemption field")
	}
}
