package runtimecache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestRuntimePartCleanupFailureRetainsOriginalWriterAndRetriesV1(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "runtime")
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item := supply.NewItem(supply.KindUserns, "fixture-launcher", "1.2.3", "amd64", []byte("original bytes"), supply.ReleaseFolder("1.2.3")+"/fixture")
	part, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(directory, part.name)
	held := original + ".retained"
	if err := os.Rename(original, held); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("foreign bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := part.Close(); err == nil {
		t.Fatal("foreign name became successful original cleanup")
	}
	if part.closed || !part.closing {
		t.Fatal("failed cleanup lost retryable original state")
	}
	if _, err := part.file.Stat(); err != nil {
		t.Fatal("failed name check dropped original writer", err)
	}
	if err := part.WriteAt([]byte("x"), 0); err == nil {
		t.Fatal("closing part accepted new data")
	}
	foreign, err := os.ReadFile(original)
	if err != nil || string(foreign) != "foreign bytes" {
		t.Fatal("cleanup touched a replacement", err)
	}
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(held, original); err != nil {
		t.Fatal(err)
	}
	if err := part.Close(); err != nil {
		t.Fatal(err)
	}
	if !part.closed || !part.removed || !part.incomingSynced || !part.writerReleased {
		t.Fatal("retry did not complete the same physical cleanup")
	}
	if err := part.Close(); err != nil {
		t.Fatal("duplicate close changed completion", err)
	}
}

func TestRuntimePartCleanupUsesSameIncomingParentAfterRenameV1(t *testing.T) {
	store, directory := testStore(t)
	item := testManifest([]byte("original bytes"), "r1").Items[0]
	part, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(directory, "incoming-retained")
	if err := os.Rename(filepath.Join(directory, "incoming"), retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "incoming"), 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(directory, part.name)
	if err := os.WriteFile(foreign, []byte("replacement parent file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := part.Close(); err != nil {
		t.Fatal("cleanup lost its actual created parent", err)
	}
	if _, err := os.Stat(filepath.Join(retained, part.basename)); !os.IsNotExist(err) {
		t.Fatal("original part remains in the retained parent", err)
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "replacement parent file" {
		t.Fatal("cleanup touched another directory's same-name file", err)
	}
	if !part.incomingSynced || !part.parentReleased || !part.closed {
		t.Fatal("original parent cleanup was not recorded")
	}
}

func TestRuntimePublicationSyncFailureRetainsOriginalDirectoryUntilRetryV1(t *testing.T) {
	store, directory := testStore(t)
	body := []byte("checked original")
	item := testManifest(body, "r1").Items[0]
	part, err := store.Begin(item)
	if err != nil {
		t.Fatal(err)
	}
	if err := part.WriteAt(body, 0); err != nil {
		t.Fatal(err)
	}
	original, err := os.Stat(filepath.Join(directory, "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("original directory sync failed")
	store.beforePartSync = func(actual *os.File) error {
		current, err := actual.Stat()
		if err != nil || !os.SameFile(current, original) {
			t.Fatal("sync changed its actual publication parent", err)
		}
		return failure
	}
	if err := part.Finish(t.Context()); !errors.Is(err, failure) {
		t.Fatal("publication sync failure lost", err)
	}
	if part.closed || part.parentReleased || part.publicationDirReleased || part.publicationSynced || !part.writerReleased {
		t.Fatal("failed sync dropped original resources or retained a writable descriptor")
	}
	if _, err := part.publicationDir.Stat(); err != nil {
		t.Fatal("original destination directory was dropped", err)
	}
	if err := part.Close(); !errors.Is(err, failure) {
		t.Fatal("close falsely acknowledged pending sync", err)
	}
	store.beforePartSync = nil
	if err := part.Close(); err != nil {
		t.Fatal(err)
	}
	if !part.closed || !part.publicationSynced || !part.publicationDirReleased || !part.parentReleased {
		t.Fatal("same successful retry did not release original directory owners")
	}
}
