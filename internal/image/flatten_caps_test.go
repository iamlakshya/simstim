package image

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFlattenCaps(t *testing.T) {
	body := flatTar(t, flatFixture{Name: "a", Body: "one"}, flatFixture{Name: "b", Body: "two"})
	cases := []struct {
		name   string
		limits flattenLimits
		count  int
	}{
		{"compressed", flattenLimits{int64(len(body)) - 1, 1 << 20, 10, 2}, 1},
		{"uncompressed", flattenLimits{1 << 20, int64(len(body)) - 1, 10, 2}, 1},
		{"entries", flattenLimits{1 << 20, 1 << 20, 2, 2}, 1},
		{"layers", flattenLimits{1 << 20, 1 << 20, 10, 1}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blobs := make([][]byte, tc.count)
			for i := range blobs {
				blobs[i] = body
			}
			layers, readers := flatInputs(blobs...)
			_, err := flattenWithLimits(context.Background(), layers, readers, &flatMemory{}, io.Discard, tc.limits)
			assertFlatCode(t, err, "image_too_large")
		})
	}
}

func TestFlattenCountsDiscardedBytes(t *testing.T) {
	// The tar reader stops at its end marker. This suffix must still consume
	// the decompression budget instead of escaping the bomb check.
	body := flatTar(t, flatFixture{Name: "a", Body: "one"})
	body = append(body, bytes.Repeat([]byte{0}, 8192)...)
	layers, readers := flatInputs(body)
	limits := flattenLimits{1 << 20, 4096, 10, 2}
	_, err := flattenWithLimits(context.Background(), layers, readers, &flatMemory{}, io.Discard, limits)
	assertFlatCode(t, err, "image_too_large")
}

func TestFlattenRefusesManifestSizeAndMediaMismatch(t *testing.T) {
	body := flatTar(t, flatFixture{Name: "a", Body: "one"})
	for _, delta := range []int64{-1, 1} {
		layers, readers := flatInputs(body)
		layers[0].Size += delta
		_, err := Flatten(context.Background(), layers, readers, &flatMemory{}, io.Discard)
		assertFlatCode(t, err, "image_bad_layer")
	}
	layers, readers := flatInputs(body)
	layers[0].MediaType = "application/octet-stream"
	_, err := Flatten(context.Background(), layers, readers, &flatMemory{}, io.Discard)
	assertFlatCode(t, err, "image_bad_layer")
}

func TestFlattenDevicesReplaceLowerContent(t *testing.T) {
	layers, readers := flatInputs(
		flatTar(t, flatFixture{Name: "dev/file", Body: "stale"}),
		flatTar(t, flatFixture{Name: "dev/file", Type: "character"}),
	)
	var out bytes.Buffer
	devices, err := Flatten(context.Background(), layers, readers, &flatMemory{}, &out)
	if err != nil {
		t.Fatal(err)
	}
	_, entries := flatResult(t, out.Bytes())
	if _, ok := entries["dev/file"]; ok || devices != 1 {
		t.Fatal("dropping a device retained the lower file")
	}
}

func TestFlattenCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	layers, readers := flatInputs(flatTar(t, flatFixture{Name: "a", Body: "one"}))
	var out bytes.Buffer
	_, err := Flatten(ctx, layers, readers, &flatMemory{}, &out)
	if !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("cancelled conversion = %v, emitted %d bytes", err, out.Len())
	}
}

func TestFlattenUnsupportedEntry(t *testing.T) {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if err := w.WriteHeader(&tar.Header{Name: "odd", Typeflag: 'Z', Mode: 0o600, Format: tar.FormatPAX}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	layers, readers := flatInputs(b.Bytes())
	_, err := Flatten(context.Background(), layers, readers, &flatMemory{}, io.Discard)
	assertFlatCode(t, err, "image_bad_layer")
}

func FuzzFlatten(f *testing.F) {
	f.Add(flatTar(f, flatFixture{Name: "file", Body: "body"}))
	f.Add(flatTar(f, flatFixture{Name: "d/file", Body: "body"}, flatFixture{Name: "d/.wh..wh..opq"}))
	f.Add([]byte("not a tar"))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 64<<10 {
			return
		}
		layers, readers := flatInputs(input)
		var first bytes.Buffer
		limits := flattenLimits{64 << 10, 64 << 10, 32, 2}
		_, err := flattenWithLimits(context.Background(), layers, readers, &flatMemory{}, &first, limits)
		if err != nil {
			return
		}
		names, entries := flatResult(t, first.Bytes())
		for _, name := range names {
			if strings.HasPrefix(name, "/") || strings.Contains(name, "../") || strings.HasPrefix(name, ".wh.") || strings.Contains(name, "/.wh.") {
				t.Fatalf("unsafe normalised name %q", name)
			}
			h := entries[name].header
			if h.Typeflag == tar.TypeChar || h.Typeflag == tar.TypeBlock {
				t.Fatal("a device entered the normalised tar")
			}
		}
		layers, readers = flatInputs(input)
		var second bytes.Buffer
		if _, err := flattenWithLimits(context.Background(), layers, readers, &flatMemory{}, &second, limits); err != nil || !bytes.Equal(first.Bytes(), second.Bytes()) {
			t.Fatalf("successful flattening is not deterministic: %v", err)
		}
	})
}
