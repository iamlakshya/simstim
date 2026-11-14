package client

import (
	"context"
	"testing"
)

func TestHostRegistrationPayloadCaptureNeedsActualOPAndCannotBorrowReplacement(t *testing.T) {
	stateFDTestHome(t)
	h := Host{Name: "box", SSH: "registration.example"}
	staged, abort, err := StagePinOwned(t.Context(), h.Name)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	observation, err := ObserveHostRegistrationContext(staged, HostChange{Kind: "host add", Now: &h})
	if err != nil {
		t.Fatal(err)
	}
	bound := WithHostStateObservation(staged, observation)
	if _, err := captureHostStateObservation(bound, h); err == nil {
		t.Fatal("a registration payload was captured without its actual original OP")
	}
	owned, unlock, err := LockHost(bound, h.Name, "registration capture")
	if err != nil {
		t.Fatal(err)
	}
	captured, err := captureHostStateObservation(owned, h)
	if err != nil || captured != observation {
		t.Fatalf("the real pre-read owner was not captured: %v", err)
	}
	fresh := WithHostStateObservation(context.Background(), captured)
	if got := fresh.Value(heldKey{h.Name}); got != owned.Value(heldKey{h.Name}) {
		t.Fatal("a fresh runtime context lost the same physical registration OP")
	}
	unlock()
	if err := requireHostStatePayloadObservation(fresh, h, captured); err == nil {
		t.Fatal("an ended registration operation retained payload authority")
	}
	if _, release, err := LockHost(WithHostStateObservation(t.Context(), captured), h.Name, "replacement wrapper"); err == nil {
		release()
		t.Fatal("the old registration token acquired a new owner around its old reply")
	}
}
