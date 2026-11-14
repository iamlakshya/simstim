package runtimecache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/supply"
)

func defaultImage(hex string) supply.Item {
	return supply.Item{Kind: supply.KindImage, Name: "agent-base", Arch: runtime.GOARCH, Digest: "sha256:" + strings.Repeat(hex, 64), RootHash: strings.Repeat("b", 64), DataBlocks: 1}
}
func imageDefaults(items ...supply.Item) *[]supply.Item {
	copy := append([]supply.Item{}, items...)
	return &copy
}

func TestImageDefaultsUseCanonicalValidationAndSeparateRoles(t *testing.T) {
	cacheTestRoot(t)
	manifest := testManifest([]byte("data"), "r1")
	valid := defaultImage("a")
	if err := ValidateWithImageDefaults(manifest, imageDefaults(valid), runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*[]supply.Item)
	}{
		{"digest", func(v *[]supply.Item) { (*v)[0].Digest = "sha256:bad" }},
		{"root hash", func(v *[]supply.Item) { (*v)[0].RootHash = "bad" }},
		{"data blocks", func(v *[]supply.Item) { (*v)[0].DataBlocks = 0 }},
		{"architecture", func(v *[]supply.Item) {
			(*v)[0].Arch = "amd64"
			if runtime.GOARCH == "amd64" {
				(*v)[0].Arch = "arm64"
			}
		}},
		{"non image", func(v *[]supply.Item) { it := manifest.Items[0]; it.Name = "not-an-image"; (*v)[0] = it }},
		{"duplicate", func(v *[]supply.Item) { *v = append(*v, (*v)[0]) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := []supply.Item{valid}
			test.change(&values)
			if err := ValidateWithImageDefaults(manifest, &values, runtime.GOARCH); err == nil {
				t.Fatal("invalid image-default metadata accepted")
			}
		})
	}
	widened := manifest
	widened.Items = append(append([]supply.Item{}, manifest.Items...), valid)
	if err := ValidateWithImageDefaults(widened, nil, runtime.GOARCH); err == nil {
		t.Fatal("image metadata widened runtime blob roles")
	}
	var nilSlice []supply.Item
	if err := ValidateWithImageDefaults(manifest, &nilSlice, runtime.GOARCH); err == nil {
		t.Fatal("nil slice encoded unknown authority as known empty")
	}
}

func TestImageDefaultsDTOAndRecordDistinguishUnknownFromKnownEmpty(t *testing.T) {
	cacheTestRoot(t)
	manifest := testManifest([]byte("data"), "r1")
	for _, known := range []bool{false, true} {
		var values *[]supply.Item
		if known {
			values = imageDefaults()
		}
		request := RuntimeRequest{Manifest: manifest, ImageDefaults: values}
		encoded, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		var got RuntimeRequest
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if (got.ImageDefaults != nil) != known {
			t.Fatalf("wire authority changed: %s", encoded)
		}
		if known && !bytes.Contains(encoded, []byte(`"image_defaults":[]`)) {
			t.Fatalf("known empty is not explicit: %s", encoded)
		}
		record := Record{Manifest: manifest, InstalledAt: time.Now(), ImageDefaults: values}
		encoded, err = json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		var loaded Record
		if err := json.Unmarshal(encoded, &loaded); err != nil {
			t.Fatal(err)
		}
		if (loaded.ImageDefaults != nil) != known {
			t.Fatalf("stored authority changed: %s", encoded)
		}
	}
	var unknown Record
	raw := []byte(`{"manifest":{},"installed_at":"2026-01-01T00:00:00Z","image_defaults":null}`)
	if err := json.Unmarshal(raw, &unknown); err != nil {
		t.Fatal(err)
	}
	if unknown.ImageDefaults != nil {
		t.Fatal("null became an authoritative empty default")
	}
}

func TestImageDefaultsLegacyRefreshPreservesParentFieldsAndAge(t *testing.T) {
	store, _ := testStore(t)
	body := []byte("data")
	manifest := testManifest(body, "r1")
	installBody(t, store, manifest.Items[0], body)
	if err := store.Commit(context.Background(), manifest, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	name := "runtime/r1.json"
	raw, err := store.root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["parent_future"] = json.RawMessage(`{"guard":"kept"}`)
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.root.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := store.Records(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	wrongRelease := manifest
	wrongRelease.Release = "v" + manifest.Release
	if err := store.CommitWithImageDefaults(context.Background(), wrongRelease, imageDefaults(defaultImage("a")), runtime.GOARCH); err == nil {
		t.Fatal("legacy authority enriched from another release identity")
	}
	values := imageDefaults(defaultImage("a"))
	if err := store.CommitWithImageDefaults(context.Background(), manifest, values, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	after, err := store.Records(runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if !before[0].InstalledAt.Equal(after[0].InstalledAt) || !reflect.DeepEqual(before[0].Manifest, after[0].Manifest) {
		t.Fatal("metadata refresh changed the parent runtime or age")
	}
	raw, err = store.root.ReadFile(name)
	if err != nil || !bytes.Contains(raw, []byte(`"parent_future":{"guard":"kept"}`)) {
		t.Fatalf("parent field lost: %s %v", raw, err)
	}
	got, err := store.ImageDefaults(context.Background(), runtime.GOARCH)
	if err != nil || !reflect.DeepEqual(got, *values) {
		t.Fatalf("refreshed image defaults: %+v %v", got, err)
	}
	if err := store.Commit(context.Background(), manifest, runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	got, err = store.ImageDefaults(context.Background(), runtime.GOARCH)
	if err != nil || !reflect.DeepEqual(got, *values) {
		t.Fatal("legacy commit erased image authority")
	}
	if err := store.CommitWithImageDefaults(context.Background(), manifest, imageDefaults(), runtime.GOARCH); err == nil {
		t.Fatal("known defaults changed for an existing label")
	}
}

func TestImageDefaultsFailClosedWithoutSelectedClientAuthority(t *testing.T) {
	for _, kind := range []string{"no labels", "legacy", "null", "malformed image", "unknown nested supply"} {
		t.Run(kind, func(t *testing.T) {
			store, _ := testStore(t)
			body := []byte("data")
			manifest := testManifest(body, "r1")
			installBody(t, store, manifest.Items[0], body)
			if kind != "no labels" {
				if err := store.Commit(context.Background(), manifest, runtime.GOARCH); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "null" || kind == "malformed image" || kind == "unknown nested supply" {
				raw, err := store.root.ReadFile("runtime/r1.json")
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]json.RawMessage
				json.Unmarshal(raw, &fields)
				switch kind {
				case "null":
					fields["image_defaults"] = json.RawMessage(`null`)
				case "malformed image":
					it := defaultImage("a")
					it.RootHash = "bad"
					fields["image_defaults"], _ = json.Marshal([]supply.Item{it})
				default:
					itemJSON, _ := json.Marshal(defaultImage("a"))
					var item map[string]json.RawMessage
					json.Unmarshal(itemJSON, &item)
					item["future_supply"] = json.RawMessage(`"reject"`)
					fields["image_defaults"], _ = json.Marshal([]map[string]json.RawMessage{item})
				}
				raw, _ = json.Marshal(fields)
				if err := store.root.WriteFile("runtime/r1.json", raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			_, err := store.ImageDefaults(context.Background(), runtime.GOARCH)
			var unavailable *ImageDefaultsUnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("missing authority was not a typed GC refusal: %v", err)
			}
		})
	}
}

func TestImageDefaultsKnownEmptyAndNewestTwoLabels(t *testing.T) {
	store, _ := testStore(t)
	now := time.Now()
	var expected []supply.Item
	for i, label := range []string{"old", "middle", "new"} {
		body := []byte(label)
		manifest := testManifest(body, label)
		installBody(t, store, manifest.Items[0], body)
		if i == 0 {
			if err := store.Commit(context.Background(), manifest, runtime.GOARCH); err != nil {
				t.Fatal(err)
			}
		} else {
			item := defaultImage([]string{"a", "c", "d"}[i])
			if err := store.CommitWithImageDefaults(context.Background(), manifest, imageDefaults(item), runtime.GOARCH); err != nil {
				t.Fatal(err)
			}
			expected = append([]supply.Item{item}, expected...)
		}
		name := "runtime/" + label + ".json"
		raw, err := store.root.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var record Record
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		record.InstalledAt = now.Add(time.Duration(i) * time.Minute)
		raw, _ = json.Marshal(record)
		if err := store.root.WriteFile(name, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ImageDefaults(context.Background(), runtime.GOARCH)
	if err != nil || !reflect.DeepEqual(got, expected) {
		t.Fatalf("newest-two authority: %+v %v", got, err)
	}
	emptyStore, _ := testStore(t)
	body := []byte("empty")
	m := testManifest(body, "empty")
	installBody(t, emptyStore, m.Items[0], body)
	if err := emptyStore.CommitWithImageDefaults(context.Background(), m, imageDefaults(), runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	got, err = emptyStore.ImageDefaults(context.Background(), runtime.GOARCH)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("explicit known empty refused: %#v %v", got, err)
	}
}

func TestImageDefaultMetadataDoesNotEnterRuntimeFilePruning(t *testing.T) {
	store, dir := testStore(t)
	body := []byte("runtime")
	m := testManifest(body, "r1")
	installBody(t, store, m.Items[0], body)
	image := defaultImage("a")
	if err := store.CommitWithImageDefaults(context.Background(), m, imageDefaults(image), runtime.GOARCH); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir + "/sha256")
	if err != nil || len(files) != 1 || files[0].Name() != m.Items[0].SHA256 {
		t.Fatalf("metadata installed an image as a runtime blob: %v %v", files, err)
	}
	if _, err := store.Prune(context.Background(), runtime.GOARCH, nil, time.Now(), true); err != nil {
		t.Fatal(err)
	}
}
