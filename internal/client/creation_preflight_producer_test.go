package client

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCreationPreflightProducerPublishesOnlyActualAbsentProof(t *testing.T) {
	stateFDTestHome(t)
	request := hostCreationRequestFixture()
	ctx, release, err := BeginHostCreationContext(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := CheckHostCreationRemovalContext(ctx, request.Name); err != nil {
		t.Fatal(err)
	}
	if err := StartHostCreationContext(ctx); err != nil {
		t.Fatalf("actual absent preflight did not admit original Start: %v", err)
	}
}

func TestCreationPreflightProducerPreservesExistingJournalAndCannotStart(t *testing.T) {
	stateFDTestHome(t)
	request := hostCreationRequestFixture()
	folder := removalDir(request.Name)
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"host":"assigned","done":["destroy"],"future":{"preserve":true}}`)
	path := filepath.Join(folder, "removal.json")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lock", nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, release, err := BeginHostCreationContext(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := CheckHostCreationRemovalContext(ctx, request.Name); err == nil {
		t.Fatal("existing history became absence/provider authority")
	}
	if err := StartHostCreationContext(ctx); err == nil {
		t.Fatal("failed preflight admitted provider start")
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, original) {
		t.Fatal("protected journal was changed")
	}
}

func TestCreationPreflightProducerRefusesForeignExpiredOrCancelledScope(t *testing.T) {
	stateFDTestHome(t)
	if err := CheckHostCreationRemovalContext(t.Context(), "assigned"); err == nil {
		t.Fatal("a public name alone became a creation preflight")
	}
	request := hostCreationRequestFixture()
	ctx, release, err := BeginHostCreationContext(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := CheckHostCreationRemovalContext(ctx, "foreign"); err == nil {
		t.Fatal("preflight borrowed another name's reservation")
	}
	ended, cancel := context.WithCancel(ctx)
	cancel()
	if err := CheckHostCreationRemovalContext(ended, request.Name); err == nil {
		t.Fatal("cancelled caller published preflight")
	}
	release()
	if err := CheckHostCreationRemovalContext(ctx, request.Name); err == nil {
		t.Fatal("expired OP renewed a preflight")
	}
}
