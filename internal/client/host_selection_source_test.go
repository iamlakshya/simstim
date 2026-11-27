package client

import (
	"os"
	"testing"
)

func TestHostSelectionSourceIsActualReadFactWithoutCheckedIdentity(t *testing.T) {
	h := hostTransportSavedFixture(t)
	source, err := captureHostSelectionSource(t.Context(), h)
	if err != nil || !source.exists || !ValidHostID(source.generation) {
		t.Fatalf("actual local selection did not capture current Source: %+v, %v", source, err)
	}
	if _, err := os.Lstat(removalDir(h.Name)); !os.IsNotExist(err) {
		t.Fatalf("local Source selection created host JSON state: %v", err)
	}
	c := finishGeneratedTransportFixture(t, h, "")
	if !sameHostTransportSource(source, c.hostTransport.source) {
		t.Fatal("same-generation actual connection changed local selection Source")
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	current, err := captureHostSelectionSource(t.Context(), h)
	if err != nil || sameHostTransportSource(source, current) {
		t.Fatalf("canonical same-content write refreshed the old selection Source: %v", err)
	}
}

func TestHostSelectionSourceNeverRefreshesCarriedOldObservation(t *testing.T) {
	h, ctx, _, release := hostObservationFixture(t)
	original, err := captureHostSelectionSource(ctx, h)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{h}); err != nil {
		t.Fatal(err)
	}
	if _, err := captureHostSelectionSource(ctx, h); err == nil {
		t.Fatal("old request selection borrowed a fresh membership operation")
	}
	fresh, err := captureHostSelectionSource(t.Context(), h)
	if err != nil || sameHostTransportSource(original, fresh) {
		t.Fatalf("identical re-add donated the old local selection: %v", err)
	}
}
