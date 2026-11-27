package image

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func imageStore(t *testing.T) *Store {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(dir, "images"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
func testConvert(ctx context.Context, layers []Layer, files []*os.File, out *os.File) error {
	_, err := Convert(ctx, layers, files, out)
	return err
}
func noImageReferences() (map[string]string, error) { return map[string]string{}, nil }

func TestPrepareTwoPullsPublishOnce(t *testing.T) {
	store := imageStore(t)
	registry, request, _, _, _ := imageRegistryFixture(t)
	var conversions atomic.Int32
	var wait sync.WaitGroup
	results := make(chan Record, 2)
	failures := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			record, err := store.Prepare(context.Background(), request, registry, func(ctx context.Context, layers []Layer, files []*os.File, out *os.File) error {
				conversions.Add(1)
				return testConvert(ctx, layers, files, out)
			})
			results <- record
			failures <- err
		}()
	}
	wait.Wait()
	close(results)
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	var root string
	for record := range results {
		if root != "" && record.RootHash != root {
			t.Fatal("same digest gave different roots")
		}
		root = record.RootHash
	}
	if conversions.Load() != 1 {
		t.Fatalf("converted %d times", conversions.Load())
	}
}

func TestWrongRootRemovesConversion(t *testing.T) {
	store := imageStore(t)
	registry, request, _, _, _ := imageRegistryFixture(t)
	request.RootHash = strings.Repeat("f", 64)
	request.DataBlocks = 1
	_, err := store.Prepare(context.Background(), request, registry, testConvert)
	assertFlatCode(t, err, "image_root_mismatch")
	if _, err := store.root.Lstat(request.PlatformDigest[7:]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrong-root image remains: %v", err)
	}
	entries, err := store.root.Open("incoming")
	if err != nil {
		t.Fatal(err)
	}
	defer entries.Close()
	children, err := entries.ReadDir(-1)
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if !strings.HasSuffix(child.Name(), ".lock") {
			t.Fatalf("partial image remains: %s", child.Name())
		}
	}
}

func TestImageRemoveAndPruneReferences(t *testing.T) {
	store := imageStore(t)
	registry, request, _, _, _ := imageRegistryFixture(t)
	record, err := store.Prepare(context.Background(), request, registry, testConvert)
	if err != nil {
		t.Fatal(err)
	}
	refs := func() (map[string]string, error) {
		return map[string]string{record.PlatformDigest: "stopped-session"}, nil
	}
	assertFlatCode(t, store.Remove(context.Background(), record.PlatformDigest, refs), "image_in_use")
	old := time.Now().Add(-8 * 24 * time.Hour)
	record.ConvertedAt = old
	record.UsedAt = old
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	file, err := store.regular(record.PlatformDigest[7:]+"/image.json", os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	file.Close()
	defaults := func() (map[string]bool, error) { return map[string]bool{record.Digest: true}, nil }
	if removed, err := store.Prune(context.Background(), noImageReferences, defaults, time.Now()); err != nil || len(removed) != 0 {
		t.Fatalf("default removed: %v %v", removed, err)
	}
	unknown := errors.New("legacy image defaults unavailable")
	if _, err := store.Prune(context.Background(), noImageReferences, func() (map[string]bool, error) { return nil, unknown }, time.Now()); !errors.Is(err, unknown) {
		t.Fatalf("unknown authority ignored: %v", err)
	}
	if removed, err := store.Prune(context.Background(), noImageReferences, func() (map[string]bool, error) { return map[string]bool{}, nil }, time.Now()); err != nil || len(removed) != 1 {
		t.Fatalf("old image not pruned: %v %v", removed, err)
	}
}

func TestStoreRefusesSymlinkAncestor(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(filepath.Join(dir, "link", "images")); err == nil {
		store.Close()
		t.Fatal("symlink ancestor accepted")
	}
}

func TestPruneReadsReferencesAndDefaultsUnderExclusiveLease(t *testing.T) {
	store := imageStore(t)
	registry, request, _, _, _ := imageRegistryFixture(t)
	record, err := store.Prepare(context.Background(), request, registry, testConvert)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	witness := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		lease, err := store.Acquire(ctx, record.PlatformDigest)
		if lease != nil {
			lease.Close()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("retention callback ran outside the exclusive image lease: %v", err)
		}
		checked++
	}
	refs := func() (map[string]string, error) { witness(); return map[string]string{}, nil }
	defaults := func() (map[string]bool, error) { witness(); return map[string]bool{}, nil }
	if _, err := store.Prune(context.Background(), refs, defaults, time.Now()); err != nil {
		t.Fatal(err)
	}
	if checked != 2 {
		t.Fatalf("retention callbacks checked %d times", checked)
	}
}

func TestPruneDuringConversionKeepsItsIncomingDescriptors(t *testing.T) {
	store := imageStore(t)
	registry, request, _, _, _ := imageRegistryFixture(t)
	entered := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := store.Prepare(context.Background(), request, registry, func(ctx context.Context, layers []Layer, files []*os.File, out *os.File) error {
			close(entered)
			<-resume
			return testConvert(ctx, layers, files, out)
		})
		done <- err
	}()
	<-entered
	removed, err := store.Prune(context.Background(), noImageReferences, func() (map[string]bool, error) { return map[string]bool{}, nil }, time.Now())
	close(resume)
	conversionErr := <-done
	if err != nil || conversionErr != nil || len(removed) != 0 {
		t.Fatalf("prune disrupted conversion: removed %v, prune %v, conversion %v", removed, err, conversionErr)
	}
	if _, err := store.readRecord(request.PlatformDigest[7:]); err != nil {
		t.Fatal(err)
	}
}

func TestOpenedImageKeepsSharedLeaseUntilTrustedReference(t *testing.T) {
	store := imageStore(t)
	registry, request, _, _, _ := imageRegistryFixture(t)
	record, err := store.Prepare(context.Background(), request, registry, testConvert)
	if err != nil {
		t.Fatal(err)
	}
	request.RootHash = record.RootHash
	request.DataBlocks = record.DataBlocks
	file, lease, opened, err := store.OpenImage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	defer lease.Close()
	if opened.RootHash != record.RootHash {
		t.Fatal("opened descriptor lost the client root check")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := store.Remove(ctx, record.PlatformDigest, noImageReferences); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("removal passed an open start lease: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	request.RootHash = strings.Repeat("f", 64)
	if other, otherLease, _, err := store.OpenImage(context.Background(), request); err == nil {
		other.Close()
		otherLease.Close()
		t.Fatal("a wrong client verity root opened for boot")
	}
}

func TestCachedStatusMarksUseWithoutChangingConversion(t *testing.T) {
	store := imageStore(t)
	registry, request, _, _, _ := imageRegistryFixture(t)
	record, err := store.Prepare(context.Background(), request, registry, testConvert)
	if err != nil {
		t.Fatal(err)
	}
	converted := record.ConvertedAt
	status, err := store.Cached(context.Background(), request)
	if err != nil || !status.Present || status.Image == nil || status.Image.UsedAt.Before(record.UsedAt) || !status.Image.ConvertedAt.Equal(converted) {
		t.Fatalf("cached use %+v, %v", status, err)
	}
	request.PlatformDigest = "sha256:" + strings.Repeat("d", 64)
	status, err = store.Cached(context.Background(), request)
	if err != nil || status.Present || status.Image != nil {
		t.Fatalf("missing cached image %+v, %v", status, err)
	}
}
