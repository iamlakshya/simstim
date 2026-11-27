package image

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func TestOwnedMetadataCaptureCrashReplaysFromExactIntent(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "captured-before-publish", true: "published-before-receipt"}[published], func(t *testing.T) {
			store, request, _ := ownedPreparedImage(t)
			stopped := errors.New("stop before metadata publication")
			store.gate = func(context.Context, func() error) error { return stopped }
			if _, err := store.Cached(t.Context(), request); !errors.Is(err, stopped) {
				t.Fatalf("intent preparation = %v", err)
			}
			receipt, err := store.readOwnership(request.PlatformDigest)
			if err != nil || receipt.Pending == nil {
				t.Fatalf("pending metadata = %+v, %v", receipt, err)
			}
			lease, err := store.lock(t.Context(), request.PlatformDigest[7:]+".lock", false)
			if err != nil {
				t.Fatal(err)
			}
			root, err := store.childRoot(request.PlatformDigest[7:])
			if err != nil {
				t.Fatal(err)
			}
			pending := receipt.Pending
			if err := captureLeaf(root, "image.json", pending.Quarantine, pending.Before, 0o600); err != nil {
				t.Fatal(err)
			}
			if published {
				if err := cachefs.RenameNoReplace(root, pending.Temporary, "image.json"); err != nil {
					t.Fatal(err)
				}
			}
			root.Close()
			lease.Close()
			store.gate = nil
			status, err := store.Cached(t.Context(), request)
			if err != nil || !status.Present {
				t.Fatalf("metadata replay = %+v, %v", status, err)
			}
			fresh, err := store.readOwnership(request.PlatformDigest)
			if err != nil || fresh.Pending != nil {
				t.Fatalf("pending receipt did not settle: %+v, %v", fresh, err)
			}
			dir, err := store.childRoot(request.PlatformDigest[7:])
			if err != nil {
				t.Fatal(err)
			}
			names, err := directoryEntries(dir)
			dir.Close()
			if err != nil || len(names) != 2 {
				t.Fatalf("metadata replay retained unaccounted variants: %v, %v", names, err)
			}
		})
	}
}

func TestOwnershipHeadCrashUsesCoherentProducerNativeWitnesses(t *testing.T) {
	isolateImageOwnership(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stopped := errors.New("publication stopped")
	converted := false
	store, err := OpenWithPublicationGate(filepath.Join(dir, "images"), func(_ context.Context, publish func() error) error {
		if !converted {
			return publish()
		}
		return stopped
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	registry, request, _, _, _ := imageRegistryFixture(t)
	if _, err := store.Prepare(t.Context(), request, registry, func(ctx context.Context, layers []Layer, files []*os.File, out *os.File) error {
		err := testConvert(ctx, layers, files, out)
		converted = true
		return err
	}); !errors.Is(err, stopped) {
		t.Fatal(err)
	}
	root, err := store.ownershipRoot()
	if err != nil {
		t.Fatal(err)
	}
	head := imageReceiptName(request.PlatformDigest)
	data, _, err := readReceipt(root, head)
	if err != nil {
		t.Fatal(err)
	}
	backup := ".receipt-old-" + strings.Repeat("a", 32)
	if err := cachefs.RenameNoReplace(root, head, backup); err != nil {
		t.Fatal(err)
	}
	prepared := ".receipt-new-" + strings.Repeat("b", 32)
	file, err := cachefs.OpenFile(root, prepared, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	file.Close()
	root.Close()
	store.gate = nil
	status, err := store.Cached(t.Context(), request)
	if err != nil || !status.Present {
		t.Fatalf("head replay = %+v, %v", status, err)
	}
	root, err = store.ownershipRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{backup, prepared} {
		if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("coherent protocol backup remains: %s, %v", name, err)
		}
	}
}

func TestUnknownReceiptBackupCannotAdoptLegacyData(t *testing.T) {
	store, request, _ := ownedPreparedImage(t)
	root, err := store.ownershipRoot()
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	name := imageReceiptName(request.PlatformDigest)
	data, _, err := readReceipt(root, name)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["future_policy"] = json.RawMessage(`"unknown"`)
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Remove(name); err != nil {
		t.Fatal(err)
	}
	backup := ".receipt-old-" + strings.Repeat("c", 32)
	file, err := cachefs.OpenFile(root, backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write(data)
	file.Close()
	report, err := store.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || len(report.Left) == 0 {
		t.Fatalf("unknown backup adopted ownership: %+v, %v", report, err)
	}
	after, err := os.ReadFile(filepath.Join(root.Name(), backup))
	if err != nil || string(after) != string(data) {
		t.Fatal("unknown protocol fields changed")
	}
	if _, err := store.readRecord(request.PlatformDigest[7:]); err != nil {
		t.Fatal("unreceipted image was removed")
	}
}

func TestOpenExistingDoesNotCreatePurgeStorage(t *testing.T) {
	isolateImageOwnership(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")
	if store, err := OpenExisting(missing); !errors.Is(err, os.ErrNotExist) {
		if store != nil {
			store.Close()
		}
		t.Fatalf("missing root was created: %v", err)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("purge opened a replacement root domain")
	}
	legacy := filepath.Join(dir, "legacy")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "keep"), []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenExisting(legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	report, err := store.PurgeOwned(t.Context(), noImageReferences)
	if err != nil || !containsImageLeft(report, "keep") {
		t.Fatalf("legacy inventory = %+v, %v", report, err)
	}
	for _, name := range []string{"incoming", "ownership"} {
		if _, err := store.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("purge created infrastructure")
		}
	}
}
