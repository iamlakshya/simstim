package client

import (
	"errors"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestSavedImagePinFailureRetainsKeptRootAndRetriesOnlyTheLocalPin(t *testing.T) {
	result := imageSaveResultFixture()
	s, peer, _ := imageSavePhaseFixture(t, t.Context(), func(request proto.Request) (any, *proto.Error) {
		if request.Op == "image.keep" {
			return result, nil
		}
		return nil, unexpectedRuntimeRequest(request)
	})
	if err := updateImageLock(s.original, func(lock *image.Lock) error {
		return recordSavedImagePin(lock, result, "a..", result.HostID, "box", false, time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Keep(s.original)
	var user *UserError
	if !errors.As(err, &user) || user.Code != "image_exists" || peer.count("image.keep", "") != 1 {
		t.Fatalf("local alias conflict lost the original kept outcome: %v %v", err, peer.snapshot())
	}
	// Repairing only the local alias lets the retained original request
	// finish its pin. It must not ask the host to keep or convert again.
	if err := updateImageLock(s.original, func(lock *image.Lock) error { lock.Images = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Keep(s.original); err != nil {
		t.Fatal("retained result could not publish its local pin", err)
	}
	if err := s.Close(); err != nil || peer.count("image.keep", "") != 1 || peer.count("image.drop", "") != 0 {
		t.Fatalf("pin recovery republished or dropped the kept image: %v %v", err, peer.snapshot())
	}
}
