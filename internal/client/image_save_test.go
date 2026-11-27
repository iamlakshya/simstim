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

func imageSaveResultFixture() image.SaveResult {
	digest := "sha256:" + strings.Repeat("b", 64)
	return image.SaveResult{HostID: strings.Repeat("a", 32), Image: image.Record{
		Ref: "a..", Digest: digest, PlatformDigest: digest, Arch: "amd64", RootHash: strings.Repeat("c", 64), DataBlocks: 1, Size: 12288, ConvertedAt: time.Unix(1, 0), UsedAt: time.Unix(1, 0), SavedAliases: []string{"a.."},
		SavedHost: &image.SavedHostV1{Version: 1, HostID: strings.Repeat("a", 32), Ref: "a..", Digest: digest}}}
}

// This is a generated accepted Source with an in-memory protocol peer. It
// exercises keep/drop publication; it neither fetches values nor runs a VM.
func imageSavePhaseFixture(t *testing.T, parent context.Context, handle func(proto.Request) (any, *proto.Error)) (*ImageSave, *runtimeDeliveryPeer, *GrantRequest) {
	t.Helper()
	h := h5GrantState(t)
	c, peer := newRuntimeDeliveryPeer(t, h.Name, handle)
	c.Host, c.hostID = h, strings.Repeat("a", 32)
	c.proc = &hostStateGeneratedProc{}
	c.route = route{t: &hostStateGeneratedTransport{}}
	c.Features = []string{proto.FeatureImageSave, "session.id"}
	owned, cleanup, attempt, err := beginHostTransportObservation(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := finishHostTransportObservation(owned, attempt, c); err != nil {
		t.Fatal(err)
	}
	r := NewImageSaveGrantRequest()
	ctx := WithGrantRequest(parent, r)
	t.Cleanup(func() { _ = r.Close() })
	guard, err := newImageSaveOperation(ctx, c, "root2345", "a..", false)
	if err != nil {
		t.Fatal(err)
	}
	s := &ImageSave{original: ctx, guard: guard, prepared: true,
		review: image.SaveReview{ID: strings.Repeat("d", 32), Ref: "a..", Size: 12288}}
	return s, peer, r
}

func assertImageSaveSelection(t *testing.T, request proto.Request, session, token string) {
	t.Helper()
	var args image.SaveSelectionArgsV1
	if err := json.Unmarshal(request.Args, &args); err != nil || args.Version != 1 || args.Session != session || args.Selection.ID != token {
		t.Errorf("selection lost original session/token: %s %v", request.Args, err)
	}
}

func TestImageSaveKeepReleasesPublicationAndPinsOnlyOriginalHost(t *testing.T) {
	result := imageSaveResultFixture()
	s, peer, _ := imageSavePhaseFixture(t, t.Context(), func(request proto.Request) (any, *proto.Error) {
		if request.Op != "image.keep" {
			return nil, unexpectedRuntimeRequest(request)
		}
		assertImageSaveSelection(t, request, "root2345", strings.Repeat("d", 32))
		// A response may wait on this same OP. The physical writer must have
		// released it before waiting for the response, as real ingress does.
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		_, release, err := LockHost(ctx, "box", "generated save response")
		if err != nil {
			t.Error("keep waited on its response while holding publication", err)
			return nil, &proto.Error{Code: "fixture", Message: "publication was retained", Hint: "Release after the frame."}
		}
		release()
		return result, nil
	})
	got, err := s.Keep(s.original)
	if err != nil || got.HostID != result.HostID {
		t.Fatalf("keep: %v", err)
	}
	if err := updateImageLock(s.original, func(lock *image.Lock) error {
		if len(lock.Images) != 1 || lock.Images[0].HostID != result.HostID || lock.Images[0].Ref != "a.." || lock.Images[0].Digest != result.Image.Digest || lock.Images[0].SignedBy != "" || lock.Images[0].Policy != "" {
			t.Errorf("saved root became a registry pin: %+v", lock.Images)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Keep(s.original); err != nil {
		t.Fatal("same successful keep lost its retained result", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil || peer.count("image.keep", "") != 1 || peer.count("image.drop", "") != 0 {
		t.Fatalf("kept image was republished or dropped: %v %v", err, peer.snapshot())
	}
}

func TestImageSaveFlaggedPhaseCannotPublishWithoutExplicitConsent(t *testing.T) {
	s, peer, _ := imageSavePhaseFixture(t, t.Context(), func(request proto.Request) (any, *proto.Error) {
		if request.Op == "image.drop" {
			assertImageSaveSelection(t, request, "root2345", strings.Repeat("d", 32))
			return nil, nil
		}
		return nil, unexpectedRuntimeRequest(request)
	})
	s.review.Flagged = []image.SaveFinding{{Path: "/etc/profile", Reason: "shell start file"}}
	view := s.Review()
	view.Flagged[0].Path = "/etc/changed"
	_, err := s.Keep(s.original)
	var user *UserError
	if !errors.As(err, &user) || user.Code != "image_flagged" || s.Review().Flagged[0].Path != "/etc/profile" || peer.count("image.keep", "") != 0 {
		t.Fatalf("review granted publication or was mutable: %v", err)
	}
	if err := s.Close(); err != nil || peer.count("image.drop", "") != 1 {
		t.Fatalf("refused original preparation not dropped: %v", err)
	}
}

func TestImageSaveCancelledPhaseDropsOriginalSelectionBeforeRequestClose(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	s, peer, r := imageSavePhaseFixture(t, parent, func(request proto.Request) (any, *proto.Error) {
		if request.Op != "image.drop" {
			return nil, unexpectedRuntimeRequest(request)
		}
		assertImageSaveSelection(t, request, "root2345", strings.Repeat("d", 32))
		return nil, nil
	})
	cancel()
	if _, err := s.Keep(s.original); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled request kept an image", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal("same-source bounded drop failed", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil || peer.count("image.drop", "") != 1 || peer.count("image.keep", "") != 0 {
		t.Fatalf("cleanup changed or repeated original selection: %v %v", err, peer.snapshot())
	}
}

func TestImageSaveUncertainKeepCannotPublishAgain(t *testing.T) {
	s, peer, _ := imageSavePhaseFixture(t, t.Context(), func(request proto.Request) (any, *proto.Error) {
		switch request.Op {
		case "image.keep":
			return nil, &proto.Error{Code: "fixture", Message: "keep result unavailable", Hint: "Inspect the original host."}
		case "image.drop":
			return nil, nil
		default:
			return nil, unexpectedRuntimeRequest(request)
		}
	})
	if _, err := s.Keep(s.original); err == nil {
		t.Fatal("failed keep returned success")
	}
	if _, err := s.Keep(s.original); err == nil || peer.count("image.keep", "") != 1 {
		t.Fatal("uncertain keep republished the prepared image", err)
	}
	if err := s.Close(); err != nil || peer.count("image.drop", "") != 1 {
		t.Fatal("failed keep lost original drop cleanup", err)
	}
}

func TestImageSaveCloseRetainsAckBeforePostResponseQuestion(t *testing.T) {
	s, peer, _ := imageSavePhaseFixture(t, t.Context(), func(request proto.Request) (any, *proto.Error) {
		if request.Op != "image.drop" {
			return nil, unexpectedRuntimeRequest(request)
		}
		assertImageSaveSelection(t, request, "root2345", strings.Repeat("d", 32))
		return nil, nil
	})
	s.guard.mu.Lock()
	review := cloneImageSaveReview(s.review)
	s.guard.review = &review
	s.guard.mu.Unlock()
	// The facade has not accepted the review yet: a current-policy question
	// arrived after its physical response. Cleanup still owns that exact ACK.
	s.review, s.prepared = image.SaveReview{}, false
	if err := s.Close(); err != nil || peer.count("image.drop", "") != 1 {
		t.Fatalf("post-response gate erased original cleanup: %v %v", err, peer.snapshot())
	}
}
