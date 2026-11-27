package image

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func TestPublicationGateRunsOnceOnlyAfterConversionUnderImageLease(t *testing.T) {
	isolateImageOwnership(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var store *Store
	converted, inside := false, false
	calls, infrastructure := 0, 0
	var digest string
	gate := func(ctx context.Context, publish func() error) error {
		if inside {
			t.Error("gate nested another publication")
		}
		if !converted {
			infrastructure++
			if _, err := store.root.Lstat(digest[7:]); !errors.Is(err, os.ErrNotExist) {
				t.Error("creator infrastructure published an image before conversion")
			}
			err := publish()
			if _, statErr := store.root.Lstat(digest[7:]); !errors.Is(statErr, os.ErrNotExist) {
				t.Error("creator infrastructure gate introduced a stable image")
			}
			return err
		}
		calls++
		leaseCtx, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
		held, err := store.Acquire(leaseCtx, digest)
		cancel()
		if held != nil {
			held.Close()
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("publication gate ran outside its image lease: %v", err)
		}
		inside = true
		defer func() { inside = false }()
		return publish()
	}
	store, err = OpenWithPublicationGate(filepath.Join(dir, "images"), gate)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry, request, _, _, _ := imageRegistryFixture(t)
	digest = request.PlatformDigest
	_, err = store.Prepare(t.Context(), request, registry, func(ctx context.Context, layers []Layer, files []*os.File, out *os.File) error {
		if inside {
			t.Error("converter ran under the publication gate")
		}
		err := testConvert(ctx, layers, files, out)
		converted = true
		return err
	})
	if err != nil || calls != 1 || infrastructure == 0 {
		t.Fatalf("initial publication = %v, stable gates %d infrastructure gates %d", err, calls, infrastructure)
	}
	if _, err := store.Cached(t.Context(), request); err != nil || calls != 2 {
		t.Fatalf("metadata touch = %v, gate calls %d", err, calls)
	}
}

func TestPreparedPublicationReplaysWithoutAnotherConversion(t *testing.T) {
	isolateImageOwnership(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("publication denied")
	converted := false
	store, err := OpenWithPublicationGate(filepath.Join(dir, "images"), func(_ context.Context, publish func() error) error {
		if !converted {
			return publish()
		}
		return denied
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry, request, _, _, _ := imageRegistryFixture(t)
	conversions := 0
	_, err = store.Prepare(t.Context(), request, registry, func(ctx context.Context, layers []Layer, files []*os.File, out *os.File) error {
		conversions++
		err := testConvert(ctx, layers, files, out)
		converted = true
		return err
	})
	if !errors.Is(err, denied) || conversions != 1 {
		t.Fatalf("denied publication = %v, conversions %d", err, conversions)
	}
	if _, err := store.root.Lstat(request.PlatformDigest[7:]); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("denied gate published a stable namespace")
	}
	receipt, err := store.readOwnership(request.PlatformDigest)
	if err != nil || !stageLeaf(receipt.Leaf) {
		t.Fatalf("prepared intent missing: %+v, %v", receipt, err)
	}
	replay, err := OpenWithPublicationGate(store.root.Name(), func(_ context.Context, publish func() error) error { return publish() })
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	status, err := replay.Cached(t.Context(), request)
	if err != nil || !status.Present || conversions != 1 {
		t.Fatalf("replay = %+v, %v, conversions %d", status, err, conversions)
	}
	if _, err := replay.root.Lstat(receipt.Leaf); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("replay left the prepared namespace behind")
	}
}

func TestMetadataIntentCanBePurgedWithoutPublishingWhenGateRefuses(t *testing.T) {
	store, request, _ := ownedPreparedImage(t)
	denied := errors.New("host is purging")
	gated, err := OpenWithPublicationGate(store.root.Name(), func(context.Context, func() error) error { return denied })
	if err != nil {
		t.Fatal(err)
	}
	defer gated.Close()
	_, err = gated.Cached(t.Context(), request)
	if !errors.Is(err, denied) {
		t.Fatalf("metadata gate refusal = %v", err)
	}
	receipt, err := gated.readOwnership(request.PlatformDigest)
	if err != nil || receipt.Pending == nil {
		t.Fatalf("metadata intent missing: %+v, %v", receipt, err)
	}
	report, err := gated.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || len(report.Left) != 0 {
		t.Fatalf("owned pending metadata could not be purged: %+v, %v", report, err)
	}
}

func TestPurgeCaptureMismatchRestoresWithoutClobber(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore", true: "collision"}[collision], func(t *testing.T) {
			store, request, _ := ownedPreparedImage(t)
			captured := ""
			store.captureHook = func(before bool, root *os.Root, from, to string) {
				if before || from != request.PlatformDigest[7:] {
					return
				}
				captured = to
				if collision {
					if err := root.Mkdir(from, 0o700); err != nil {
						t.Fatal(err)
					}
					file, err := root.OpenFile(from+"/foreign", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = file.Write([]byte("keep"))
					file.Close()
				}
				file, err := root.OpenFile(to+"/image.json", os.O_WRONLY, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteAt([]byte("x"), 0)
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			report, err := store.PurgeOwned(t.Context(), noImageReferences)
			if err != nil || len(report.Left) == 0 {
				t.Fatalf("captured mismatch was claimed clean: %+v, %v", report, err)
			}
			if collision {
				if _, err := store.root.Lstat(captured); err != nil {
					t.Fatal("mismatching capture was unlinked after restore collision")
				}
				if data, err := os.ReadFile(filepath.Join(store.root.Name(), request.PlatformDigest[7:], "foreign")); err != nil || string(data) != "keep" {
					t.Fatal("restore clobbered the foreign replacement")
				}
			} else {
				if _, err := store.root.Lstat(captured); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("successful restore left the captured name")
				}
				if data, err := os.ReadFile(filepath.Join(store.root.Name(), request.PlatformDigest[7:], "image.json")); err != nil || len(data) == 0 || data[0] != 'x' {
					t.Fatal("mismatching metadata was deleted instead of restored")
				}
			}
		})
	}
}

func TestPurgeQuarantineCrashReplaysIdempotently(t *testing.T) {
	store, request, _ := ownedPreparedImage(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store.captureHook = func(before bool, _ *os.Root, from, _ string) {
		if !before && from == request.PlatformDigest[7:] {
			cancel()
		}
	}
	report, err := store.PurgeOwned(ctx, noImageReferences)
	if !errors.Is(err, context.Canceled) || len(report.Left) == 0 {
		t.Fatalf("interrupted purge = %+v, %v", report, err)
	}
	store.captureHook = nil
	receipt, err := store.readOwnership(request.PlatformDigest)
	if err != nil || receipt.Purge == nil {
		t.Fatalf("purge intent missing: %+v, %v", receipt, err)
	}
	q, err := store.childRoot(receipt.Purge.Quarantine)
	if err != nil {
		t.Fatal(err)
	}
	if err := receipt.Directory.CheckRoot(q); err != nil {
		t.Fatal(err)
	}
	q.Close()
	report, err = store.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || len(report.Left) != 0 {
		t.Fatalf("crash replay = %+v, %v", report, err)
	}
	if _, err := store.root.Lstat(receipt.Purge.Quarantine); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("captured namespace remains after replay")
	}
}

func TestPurgeRefusesSameBytesOnAnotherMetadataInode(t *testing.T) {
	store, request, _ := ownedPreparedImage(t)
	root, err := store.childRoot(request.PlatformDigest[7:])
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	original, err := cachefs.OpenFile(root, "image.json", os.O_RDONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(original.Name())
	original.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Rename("image.json", "foreign-old"); err != nil {
		t.Fatal(err)
	}
	replacement, err := cachefs.OpenFile(root, "image.json", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = replacement.Write(data)
	replacement.Close()
	if err := root.Remove("foreign-old"); err != nil {
		t.Fatal(err)
	}
	report, err := store.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || len(report.Left) == 0 {
		t.Fatalf("matching bytes laundered ownership: %+v, %v", report, err)
	}
	if after, err := os.ReadFile(filepath.Join(store.root.Name(), request.PlatformDigest[7:], "image.json")); err != nil || string(after) != string(data) {
		t.Fatal("replacement metadata was removed")
	}
}
