package image

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/supply"
)

// Package versions here are explicit unit-fixture metadata. These tests do
// not claim to build/run Debian or record any real default guest tool version.
func defaultFixtureInputs(t *testing.T) (DefaultInputsV1, *supply.Lock) {
	t.Helper()
	lock, err := supply.LoadLockFile("../../guest/supply.lock")
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := DefaultRecipe()
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := time.Parse("20060102T150405Z", lock.Builder.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	inputs := DefaultInputsV1{Version: 1, Arch: "amd64", BaseImage: lock.Builder.Image, Snapshot: lock.Builder.Snapshot, Suite: lock.Builder.Suite, InReleaseSHA256: lock.Builder.InReleaseSHA256, RecipeSHA256: DefaultRecipeSHA256(), Epoch: stamp.Unix()}
	for _, name := range recipe.Packages {
		inputs.Packages = append(inputs.Packages, DefaultPackageV1{Name: name, Version: "0+unit-fixture", Arch: "amd64"})
	}
	return inputs, lock
}

func prepareDefaultFixture(t *testing.T, body string) (string, PreparedDefaultV1, *supply.Lock) {
	t.Helper()
	inputs, lock := defaultFixtureInputs(t)
	rootTar, err := os.CreateTemp(t.TempDir(), "root-tar")
	if err != nil {
		t.Fatal(err)
	}
	defer rootTar.Close()
	if _, err := rootTar.Write(flatTar(t, flatFixture{Name: "etc", Type: "directory", Mode: 0755}, flatFixture{Name: "etc/config", Body: body, Mode: 0644})); err != nil {
		t.Fatal(err)
	}
	if _, err := rootTar.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(parent, "prepared")
	prepared, err := PrepareDefault(context.Background(), rootTar, inputs, lock, directory)
	if err != nil {
		t.Fatal(err)
	}
	return directory, prepared, lock
}

func TestDefaultImageFixtureProducesActualOCIAndConverterCheckValues(t *testing.T) {
	a, first, lock := prepareDefaultFixture(t, "deterministic root\n")
	_, second, _ := prepareDefaultFixture(t, "deterministic root\n")
	if first.Image.Digest != second.Image.Digest || first.Layer.Digest != second.Layer.Digest || first.ConfigDigest != second.ConfigDigest || first.Image.RootHash != second.Image.RootHash || first.Image.DataBlocks != second.Image.DataBlocks {
		t.Fatal("the same root/input tuple did not produce deterministic OCI and verity checks")
	}
	checked, err := CheckDefault(t.Context(), a, lock)
	if err != nil {
		t.Fatal(err)
	}
	item, err := checked.SupplyItem()
	if err != nil || item.Kind != supply.KindImage || item.Name != "agent-base" || item.Digest != first.Image.Digest || item.RootHash != first.Image.RootHash || item.DataBlocks != first.Image.DataBlocks || item.URL != "" || item.SHA256 != "" {
		t.Fatal("the release image fields did not come from actual artifacts", err)
	}
	if _, err := new(CheckedDefaultV1).SupplyItem(); err == nil {
		t.Fatal("raw metadata created a checked release projection")
	}
}

func TestDefaultImageRejectsTamperedInputsMetadataAndActualBlobBytes(t *testing.T) {
	for _, kind := range []string{"snapshot", "recipe", "package", "architecture", "image-id", "root-hash", "root-bytes", "layer-bytes", "future-field"} {
		t.Run(kind, func(t *testing.T) {
			directory, prepared, lock := prepareDefaultFixture(t, "checked root\n")
			metadata := filepath.Join(directory, "prepared.json")
			switch kind {
			case "snapshot":
				prepared.Inputs.Snapshot = "20261109T000000Z"
			case "recipe":
				prepared.Inputs.RecipeSHA256 = strings.Repeat("f", 64)
			case "package":
				prepared.Inputs.Packages = prepared.Inputs.Packages[:len(prepared.Inputs.Packages)-1]
			case "architecture":
				prepared.Inputs.Arch = "arm64"
			case "image-id":
				prepared.Image.Digest = prepared.ConfigDigest
				prepared.Image.PlatformDigest = prepared.ConfigDigest
			case "root-hash":
				prepared.Image.RootHash = strings.Repeat("f", 64)
			case "root-bytes":
				name := filepath.Join(directory, "rootfs.img")
				if err := os.Chmod(name, 0o600); err != nil {
					t.Fatal(err)
				}
				file, err := os.OpenFile(name, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteAt([]byte("changed"), 0)
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "layer-bytes":
				name := filepath.Join(directory, "blobs/sha256", prepared.Layer.Digest[7:])
				if err := os.Chmod(name, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(name, []byte("different"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "root-bytes" && kind != "layer-bytes" {
				data, err := json.Marshal(prepared)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "future-field" {
					data = []byte(strings.Replace(string(data), `"v":1`, `"v":1,"future":true`, 1))
				}
				if err := os.Chmod(metadata, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(metadata, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := CheckDefault(t.Context(), directory, lock); err == nil {
				t.Fatal("tampered prepared input became a release pin")
			}
		})
	}
}
