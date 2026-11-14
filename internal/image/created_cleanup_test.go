package image

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func TestTransferClosePreservesUnknownAndReplacementParts(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreign-child", true: "replacement-part"}[replacement], func(t *testing.T) {
			isolateImageOwnership(t)
			store := imageStore(t)
			_, request, _, layers, _ := imageRegistryFixture(t)
			transfer, err := store.Begin(request)
			if err != nil {
				t.Fatal(err)
			}
			if err := transfer.WriteAt(t.Context(), layers[0].Digest, 0, []byte("owned")); err != nil {
				t.Fatal(err)
			}
			token := transfer.Progress().Token
			relative := "incoming/" + token + "/foreign"
			if replacement {
				original := "incoming/" + token + "/" + layers[0].Digest[7:] + ".part"
				if err := store.root.Rename(original, original+"-saved"); err != nil {
					t.Fatal(err)
				}
				relative = original
			}
			foreign, err := store.regular(relative, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := foreign.Write([]byte("keep")); err != nil {
				t.Fatal(err)
			}
			foreign.Close()
			if err := transfer.Close(); err == nil {
				t.Fatal("uncertain transfer cleanup was claimed complete")
			}
			if data, err := os.ReadFile(filepath.Join(store.root.Name(), filepath.FromSlash(relative))); err != nil || string(data) != "keep" {
				t.Fatal("transfer cleanup removed foreign bytes")
			}
			if _, err := store.root.Lstat("incoming/" + token); err != nil {
				t.Fatal("nonempty or replaced transfer directory was removed")
			}
		})
	}
}

func TestTransferCloseDoesNotRemoveAnotherEmptyTokenRoot(t *testing.T) {
	isolateImageOwnership(t)
	store := imageStore(t)
	_, request, _, _, _ := imageRegistryFixture(t)
	transfer, err := store.Begin(request)
	if err != nil {
		t.Fatal(err)
	}
	token := transfer.Progress().Token
	if err := store.root.Rename("incoming/"+token, "incoming/"+token+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err := store.mkdir("incoming/" + token); err != nil {
		t.Fatal(err)
	}
	if err := transfer.Close(); err == nil {
		t.Fatal("replacement token root was adopted")
	}
	if _, err := store.root.Lstat("incoming/" + token); err != nil {
		t.Fatal("empty foreign root was deleted")
	}
}

func TestCreatedFileCleanupChecksCapturedNativeDescriptor(t *testing.T) {
	isolateImageOwnership(t)
	store := imageStore(t)
	file, err := store.regular("incoming/owned.part", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	created, err := store.captureCreated("incoming/owned.part", file, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("growing creator bytes")); err != nil {
		t.Fatal(err)
	}
	if err := store.root.Rename("incoming/owned.part", "incoming/original-saved"); err != nil {
		t.Fatal(err)
	}
	replacement, err := store.regular("incoming/owned.part", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = replacement.Write([]byte("keep"))
	replacement.Close()
	if err := store.cleanupCreated(created, 0o600); err == nil {
		t.Fatal("replacement filename became cleanup authority")
	}
	if data, err := os.ReadFile(filepath.Join(store.root.Name(), "incoming", "owned.part")); err != nil || string(data) != "keep" {
		t.Fatal("foreign replacement was erased")
	}
}

func TestFetchedLayerKeepsReadOnlyNamedSingleLinkThroughConverter(t *testing.T) {
	isolateImageOwnership(t)
	store := imageStore(t)
	registry, request, _, _, _ := imageRegistryFixture(t)
	seen := 0
	_, err := store.Prepare(t.Context(), request, registry, func(ctx context.Context, layers []Layer, files []*os.File, out *os.File) error {
		for _, file := range files {
			if _, err := cachefs.ValidateDescriptor(file, 0o600); err != nil {
				return err
			}
			if _, err := file.WriteAt([]byte("x"), 0); err == nil {
				return errors.New("converter input descriptor is writable")
			}
			if _, err := file.Seek(0, io.SeekStart); err != nil {
				return err
			}
			seen++
		}
		return testConvert(ctx, layers, files, out)
	})
	if err != nil || seen == 0 {
		t.Fatalf("read-only input lifetime = %d, %v", seen, err)
	}
	incoming, err := store.childRoot("incoming")
	if err != nil {
		t.Fatal(err)
	}
	defer incoming.Close()
	names, err := directoryEntries(incoming)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if filepath.Ext(name) != ".lock" {
			t.Fatalf("verified input part remains after conversion: %s", name)
		}
	}
}
