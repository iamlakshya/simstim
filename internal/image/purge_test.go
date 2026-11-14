package image

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func isolateImageOwnership(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		dir := filepath.Join(root, key)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
}
func ownedPreparedImage(t *testing.T) (*Store, Request, Record) {
	t.Helper()
	isolateImageOwnership(t)
	store := imageStore(t)
	registry, request, _, _, _ := imageRegistryFixture(t)
	record, err := store.Prepare(t.Context(), request, registry, testConvert)
	if err != nil {
		t.Fatal(err)
	}
	return store, request, record
}
func containsImageLeft(report PurgeReport, path string) bool {
	for _, left := range report.Left {
		if left.Path == path {
			return true
		}
	}
	return false
}

func TestPurgeOwnedRemovesRecentImagesAndCreatorLocksWithoutPrune(t *testing.T) {
	store, request, record := ownedPreparedImage(t)
	if time.Since(record.ConvertedAt) > time.Hour {
		t.Fatal("fixture is not a recent image")
	}
	report, err := store.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || len(report.Left) != 0 || !slices.Contains(report.Removed, request.PlatformDigest) {
		t.Fatalf("owned total purge = %+v, %v", report, err)
	}
	for _, name := range []string{request.PlatformDigest[7:], request.PlatformDigest[7:] + ".lock"} {
		if _, err := store.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned namespace %s remains: %v", name, err)
		}
	}
	for _, name := range []string{"incoming", "ownership"} {
		root, err := store.childRoot(name)
		if err != nil {
			t.Fatal(err)
		}
		children, err := directoryEntries(root)
		root.Close()
		if err != nil || len(children) != 0 {
			t.Fatalf("owned infrastructure files remain in %s: %v, %v", name, children, err)
		}
	}
	again, err := store.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || len(again.Removed) != 0 || len(again.Left) != 0 {
		t.Fatalf("repeat total purge = %+v, %v", again, err)
	}
}

func TestPurgeOwnedReadsReferencesUnderExistingExclusiveLease(t *testing.T) {
	store, request, _ := ownedPreparedImage(t)
	checked := 0
	refs := func() (map[string]string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
		defer cancel()
		lease, err := store.Acquire(ctx, request.PlatformDigest)
		if lease != nil {
			lease.Close()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("reference callback ran outside its exclusive lease: %v", err)
		}
		checked++
		return map[string]string{request.PlatformDigest: "stopped-session"}, nil
	}
	report, err := store.PurgeOwned(t.Context(), refs)
	if err != nil || checked != 1 || len(report.Removed) != 0 || !containsImageLeft(report, request.PlatformDigest[7:]) {
		t.Fatalf("referenced purge = %+v, %v (checks %d)", report, err, checked)
	}
	if _, err := store.readRecord(request.PlatformDigest[7:]); err != nil {
		t.Fatal("referenced image changed")
	}
	unknown := errors.New("trusted references cannot be read")
	_, err = store.PurgeOwned(t.Context(), func() (map[string]string, error) { return nil, unknown })
	if !errors.Is(err, unknown) {
		t.Fatalf("lost reference authority error: %v", err)
	}
}

func TestPurgeOwnedWaitsForSharedStartLease(t *testing.T) {
	store, request, _ := ownedPreparedImage(t)
	file, lease, _, err := store.OpenImage(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	defer lease.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	report, err := store.PurgeOwned(ctx, noImageReferences)
	if !errors.Is(err, context.DeadlineExceeded) || len(report.Removed) != 0 {
		t.Fatalf("purge passed a shared start lease: %+v, %v", report, err)
	}
}

func TestPurgeOwnedNeverAdoptsLegacyPairAndCachedUseStaysReadOnly(t *testing.T) {
	store, request, record := ownedPreparedImage(t)
	ownership, err := store.ownershipRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err := ownership.Remove(imageReceiptName(request.PlatformDigest)); err != nil {
		t.Fatal(err)
	}
	ownership.Close()
	before, err := os.ReadFile(filepath.Join(store.root.Name(), request.PlatformDigest[7:], "image.json"))
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.Cached(t.Context(), request)
	if err != nil || !status.Present || !status.Image.UsedAt.Equal(record.UsedAt) {
		t.Fatalf("legacy cache hit changed semantics: %+v, %v", status, err)
	}
	after, err := os.ReadFile(filepath.Join(store.root.Name(), request.PlatformDigest[7:], "image.json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("cached hit rewrote or adopted legacy metadata")
	}
	report, err := store.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || !containsImageLeft(report, request.PlatformDigest[7:]) {
		t.Fatalf("legacy pair was not preserved: %+v, %v", report, err)
	}
	if _, err := store.readRecord(request.PlatformDigest[7:]); err != nil {
		t.Fatal("legacy pair was removed")
	}
}

func TestPurgeOwnedPreservesUnknownChildrenAndIncomingParts(t *testing.T) {
	store, request, _ := ownedPreparedImage(t)
	foreign, err := store.regular(request.PlatformDigest[7:]+"/foreign", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.Write([]byte("keep")); err != nil {
		t.Fatal(err)
	}
	foreign.Close()
	part, err := store.regular("incoming/legacy.part", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	part.Close()
	report, err := store.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || !containsImageLeft(report, request.PlatformDigest[7:]) || !containsImageLeft(report, "incoming/legacy.part") {
		t.Fatalf("unknown objects were not reported: %+v, %v", report, err)
	}
	if data, err := os.ReadFile(filepath.Join(store.root.Name(), request.PlatformDigest[7:], "foreign")); err != nil || string(data) != "keep" {
		t.Fatal("foreign child changed")
	}
}

func TestPurgeOwnedPreservesUnknownReceiptAndReferenceFields(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(map[bool]string{false: "receipt", true: "native witness"}[nested], func(t *testing.T) {
			store, request, _ := ownedPreparedImage(t)
			path := filepath.Join(store.root.Name(), "ownership", imageReceiptName(request.PlatformDigest))
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]json.RawMessage
			if err := json.Unmarshal(data, &root); err != nil {
				t.Fatal(err)
			}
			if nested {
				var disk map[string]json.RawMessage
				_ = json.Unmarshal(root["rootfs"], &disk)
				var ref map[string]json.RawMessage
				_ = json.Unmarshal(disk["ref"], &ref)
				ref["future_policy"] = json.RawMessage(`"keep"`)
				disk["ref"], _ = json.Marshal(ref)
				root["rootfs"], _ = json.Marshal(disk)
			} else {
				root["future_policy"] = json.RawMessage(`"keep"`)
			}
			data, err = json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			report, err := store.PurgeOwned(t.Context(), noImageReferences)
			if err != nil || len(report.Left) == 0 {
				t.Fatalf("unknown ownership was adopted: %+v, %v", report, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(data) {
				t.Fatal("future ownership metadata was erased")
			}
			if _, err := store.readRecord(request.PlatformDigest[7:]); err != nil {
				t.Fatal("unknown-reference image changed")
			}
		})
	}
}

func TestUsedAtRewritePreservesFutureImageMetadata(t *testing.T) {
	store, request, record := ownedPreparedImage(t)
	lease, err := store.lock(t.Context(), request.PlatformDigest[7:]+".lock", false)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.readOwnership(request.PlatformDigest)
	if err != nil {
		t.Fatal(err)
	}
	root, err := store.childRoot(request.PlatformDigest[7:])
	if err != nil {
		t.Fatal(err)
	}
	file, err := cachefs.OpenFile(root, "image.json", os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	fields["future_note"] = json.RawMessage(`{"keep":true}`)
	raw, _ = json.Marshal(fields)
	if err := file.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt(raw, 0); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	receipt.Metadata, err = witnessFile(root, "image.json", file, 0o600, true)
	file.Close()
	root.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.writeOwnership(receipt); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	status, err := store.Cached(t.Context(), request)
	if err != nil || !status.Present || status.Image.UsedAt.Before(record.UsedAt) {
		t.Fatalf("cached touch failed: %+v, %v", status, err)
	}
	after, err := os.ReadFile(filepath.Join(store.root.Name(), request.PlatformDigest[7:], "image.json"))
	if err != nil || !strings.Contains(string(after), `"future_note"`) {
		t.Fatal("metadata publication lost future fields")
	}
}
