package image

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"io"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/klauspost/compress/zstd"
)

type tarItem struct {
	hdr  tar.Header
	body string
}

func makeTarBytes(t *testing.T, items []tarItem) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, it := range items {
		h := it.hdr
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(it.body))
		}
		if h.Mode == 0 {
			if h.Typeflag == tar.TypeDir {
				h.Mode = 0o755
			} else {
				h.Mode = 0o644
			}
		}
		if h.Format == tar.FormatUnknown {
			h.Format = tar.FormatPAX
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatalf("write header %q: %v", h.Name, err)
		}
		if h.Typeflag == tar.TypeReg && len(it.body) > 0 {
			if _, err := io.WriteString(tw, it.body); err != nil {
				t.Fatalf("write body %q: %v", h.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func makeGzipLayer(t *testing.T, items []tarItem, mt types.MediaType) v1.Layer {
	t.Helper()
	raw := makeTarBytes(t, items)
	if mt == "" {
		mt = types.OCILayer
	}
	l, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		var gz bytes.Buffer
		w := gzip.NewWriter(&gz)
		_, _ = w.Write(raw)
		_ = w.Close()
		return io.NopCloser(bytes.NewReader(gz.Bytes())), nil
	}, tarball.WithMediaType(mt), tarball.WithCompressedCaching)
	if err != nil {
		t.Fatalf("make gzip layer: %v", err)
	}
	return l
}

func makeUncompressedLayer(t *testing.T, items []tarItem, mt types.MediaType) v1.Layer {
	t.Helper()
	raw := makeTarBytes(t, items)
	if mt == "" {
		mt = types.OCIUncompressedLayer
	}
	l, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(raw)), nil
	}, tarball.WithMediaType(mt))
	if err != nil {
		t.Fatalf("make uncompressed layer: %v", err)
	}
	return l
}

func makeZstdLayer(t *testing.T, items []tarItem) v1.Layer {
	t.Helper()
	raw := makeTarBytes(t, items)
	var out bytes.Buffer
	zw, err := zstd.NewWriter(&out)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("zstd write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	zbytes := out.Bytes()
	l, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(zbytes)), nil
	}, tarball.WithMediaType(types.OCILayerZStd), tarball.WithCompressedCaching)
	if err != nil {
		t.Fatalf("make zstd layer: %v", err)
	}
	return l
}

func makeImage(t *testing.T, layers ...v1.Layer) v1.Image {
	t.Helper()
	img, err := mutate.AppendLayers(empty.Image, layers...)
	if err != nil {
		t.Fatalf("append layers: %v", err)
	}
	return img
}

func readTarMap(t *testing.T, raw []byte) map[string]tarItem {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(raw))
	out := map[string]tarItem{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read flattened tar: %v", err)
		}
		var b []byte
		if h.Typeflag == tar.TypeReg {
			b, err = io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read body %q: %v", h.Name, err)
			}
		}
		out[h.Name] = tarItem{hdr: *h, body: string(b)}
	}
	return out
}

func TestFlattenWhiteoutsOpaqueDevicesAndMetadata(t *testing.T) {
	t1 := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	t2 := time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)

	base := makeGzipLayer(t, []tarItem{
		{hdr: tar.Header{Name: "etc", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: t1}},
		{hdr: tar.Header{
			Name:       "etc/keep.txt",
			Mode:       0o640,
			Uid:        100,
			Gid:        200,
			Uname:      "alice",
			Gname:      "staff",
			ModTime:    t1,
			AccessTime: t2,
			ChangeTime: t2,
			PAXRecords: map[string]string{
				"SCHILY.xattr.user.tag":            "ok",
				"SCHILY.xattr.trusted.overlay.opq": "y",
			},
		}, body: "base"},
		{hdr: tar.Header{Name: "etc/drop.txt"}, body: "gone"},
		{hdr: tar.Header{Name: "dir", Typeflag: tar.TypeDir}},
		{hdr: tar.Header{Name: "dir/old.txt"}, body: "old"},
		{hdr: tar.Header{Name: "dev", Typeflag: tar.TypeDir}},
		{hdr: tar.Header{Name: "dev/null", Typeflag: tar.TypeChar, Mode: 0o666, Devmajor: 1, Devminor: 3}},
		{hdr: tar.Header{Name: "dev/sda", Typeflag: tar.TypeBlock, Mode: 0o660, Devmajor: 8, Devminor: 0}},
		{hdr: tar.Header{Name: "run/pipe", Typeflag: tar.TypeFifo, Mode: 0o600}},
		{hdr: tar.Header{Name: "usr/bin/orig"}, body: "same-inode"},
		{hdr: tar.Header{Name: "usr/bin/hard", Typeflag: tar.TypeLink, Linkname: "usr/bin/orig"}},
		{hdr: tar.Header{Name: "usr/bin/sym", Typeflag: tar.TypeSymlink, Linkname: "orig"}},
	}, types.DockerLayer)

	upper := makeZstdLayer(t, []tarItem{
		{hdr: tar.Header{Name: "etc/.wh.drop.txt"}},
		{hdr: tar.Header{Name: "dir/.wh..wh..opq"}},
		{hdr: tar.Header{Name: "dir/new.txt", ModTime: t2}, body: "fresh"},
	})

	img := makeImage(t, base, upper)
	var out bytes.Buffer
	stats, err := Flatten(img, "example.com/test:1", Limits{}, &out)
	if err != nil {
		t.Fatalf("Flatten failed: %v", err)
	}
	if stats.DroppedDevices != 2 {
		t.Fatalf("DroppedDevices = %d, want 2", stats.DroppedDevices)
	}

	got := readTarMap(t, out.Bytes())
	if _, ok := got["etc/drop.txt"]; ok {
		t.Error("whited-out etc/drop.txt survived")
	}
	if _, ok := got["dir/old.txt"]; ok {
		t.Error("opaque-hidden dir/old.txt survived")
	}
	if got["dir/new.txt"].body != "fresh" {
		t.Errorf("dir/new.txt = %q, want fresh", got["dir/new.txt"].body)
	}
	if _, ok := got["dev/null"]; ok {
		t.Error("char device dev/null survived")
	}
	if _, ok := got["dev/sda"]; ok {
		t.Error("block device dev/sda survived")
	}
	for name := range got {
		if strings.Contains(name, ".wh.") {
			t.Errorf("whiteout marker %q entered output", name)
		}
	}

	keep := got["etc/keep.txt"].hdr
	if keep.Format != tar.FormatPAX {
		t.Errorf("format = %v, want PAX", keep.Format)
	}
	if keep.Uname != "" || keep.Gname != "" {
		t.Errorf("uname/gname kept: %q/%q", keep.Uname, keep.Gname)
	}
	if !keep.AccessTime.IsZero() || !keep.ChangeTime.IsZero() {
		t.Errorf("atime/ctime kept: %v / %v", keep.AccessTime, keep.ChangeTime)
	}
	if keep.PAXRecords["SCHILY.xattr.user.tag"] != "ok" {
		t.Errorf("user xattr lost: %v", keep.PAXRecords)
	}
	if _, bad := keep.PAXRecords["SCHILY.xattr.trusted.overlay.opq"]; bad {
		t.Errorf("trusted xattr survived: %v", keep.PAXRecords)
	}
	if got["usr/bin/hard"].hdr.Typeflag != tar.TypeLink || got["usr/bin/hard"].hdr.Linkname != "usr/bin/orig" {
		t.Errorf("intra-layer hard link = %+v", got["usr/bin/hard"].hdr)
	}
	if got["usr/bin/sym"].hdr.Typeflag != tar.TypeSymlink || got["usr/bin/sym"].hdr.Linkname != "orig" {
		t.Errorf("symlink = %+v", got["usr/bin/sym"].hdr)
	}
	if got["run/pipe"].hdr.Typeflag != tar.TypeFifo {
		t.Errorf("fifo = %+v", got["run/pipe"].hdr)
	}
}

func TestFlattenRejectsBadMediaType(t *testing.T) {
	bad := makeGzipLayer(t, []tarItem{{hdr: tar.Header{Name: "a"}, body: "x"}}, types.DockerForeignLayer)
	img := makeImage(t, bad)
	var out bytes.Buffer
	_, err := Flatten(img, "example.com/bad:1", Limits{}, &out)
	ie, ok := AsError(err)
	if !ok || ie.Code != CodeBadLayer {
		t.Fatalf("got err=%v, want %s", err, CodeBadLayer)
	}
}

func TestFlattenRejectsDotDotNULAndLongPaths(t *testing.T) {
	t.Run("escape path", func(t *testing.T) {
		l := makeUncompressedLayer(t, []tarItem{{hdr: tar.Header{Name: "../etc/passwd"}, body: "x"}}, types.OCIUncompressedLayer)
		var out bytes.Buffer
		_, err := Flatten(makeImage(t, l), "test:1", Limits{}, &out)
		ie, ok := AsError(err)
		if !ok || ie.Code != CodeBadLayer {
			t.Fatalf("got err=%v, want %s", err, CodeBadLayer)
		}
	})

	t.Run("filter rejects internal dotdot", func(t *testing.T) {
		raw := makeTarBytes(t, []tarItem{{hdr: tar.Header{Name: "a/../b"}, body: "x"}})
		var out bytes.Buffer
		_, err := FilterTar(bytes.NewReader(raw), &out, "test:1", Limits{})
		ie, ok := AsError(err)
		if !ok || ie.Code != CodeBadLayer {
			t.Fatalf("got err=%v, want %s", err, CodeBadLayer)
		}
	})

	t.Run("nul byte", func(t *testing.T) {
		// Patch a PAX path record to contain a NUL byte (Go's tar.Writer refuses to emit one directly).
		placeholder := "etc/aXb" + strings.Repeat("c", 105)
		raw := makeTarBytes(t, []tarItem{{hdr: tar.Header{Name: placeholder}, body: "x"}})
		idx := bytes.Index(raw, []byte("path=etc/aXb"))
		if idx < 0 {
			t.Fatal("PAX path record not found in test tar")
		}
		raw[idx+len("path=etc/a")] = 0
		var out bytes.Buffer
		_, err := FilterTar(bytes.NewReader(raw), &out, "test:1", Limits{})
		ie, ok := AsError(err)
		if !ok || ie.Code != CodeBadLayer {
			t.Fatalf("got err=%v, want %s", err, CodeBadLayer)
		}
	})

	t.Run("path over 4096 bytes", func(t *testing.T) {
		longName := strings.Repeat("a/", 2050) + "x"
		raw := makeTarBytes(t, []tarItem{{hdr: tar.Header{Name: longName}, body: "x"}})
		var out bytes.Buffer
		_, err := FilterTar(bytes.NewReader(raw), &out, "test:1", Limits{})
		ie, ok := AsError(err)
		if !ok || ie.Code != CodeBadLayer {
			t.Fatalf("got err=%v, want %s", err, CodeBadLayer)
		}
	})
}

// TestFlattenMutateExtractHardLinkDifferences verifies and documents how
// go-containerregistry's mutate.Extract behaves on cross-layer hard links and
// deleted hard-link targets vs spec 20.2 rule 5.
func TestFlattenMutateExtractHardLinkDifferences(t *testing.T) {
	t.Run("target deleted in upper layer fails closed", func(t *testing.T) {
		base := makeUncompressedLayer(t, []tarItem{
			{hdr: tar.Header{Name: "a.txt"}, body: "secret-content"},
			{hdr: tar.Header{Name: "b.txt", Typeflag: tar.TypeLink, Linkname: "a.txt"}},
		}, types.OCIUncompressedLayer)
		upper := makeUncompressedLayer(t, []tarItem{
			{hdr: tar.Header{Name: ".wh.a.txt"}},
		}, types.OCIUncompressedLayer)
		var out bytes.Buffer
		_, err := Flatten(makeImage(t, base, upper), "test:hardlink-del", Limits{}, &out)
		ie, ok := AsError(err)
		if !ok || ie.Code != CodeBadLayer {
			t.Fatalf("got err=%v, want %s (dangling hard link after mutate.Extract drops whited-out target)", err, CodeBadLayer)
		}
	})

	t.Run("cross-layer hard link emitted before target fails closed", func(t *testing.T) {
		base := makeUncompressedLayer(t, []tarItem{
			{hdr: tar.Header{Name: "a.txt"}, body: "hello"},
		}, types.OCIUncompressedLayer)
		upper := makeUncompressedLayer(t, []tarItem{
			{hdr: tar.Header{Name: "b.txt", Typeflag: tar.TypeLink, Linkname: "a.txt"}},
		}, types.OCIUncompressedLayer)
		var out bytes.Buffer
		_, err := Flatten(makeImage(t, base, upper), "test:hardlink-cross", Limits{}, &out)
		ie, ok := AsError(err)
		if !ok || ie.Code != CodeBadLayer {
			t.Fatalf("got err=%v, want %s (reverse layer order emits link before target)", err, CodeBadLayer)
		}
	})
}

func TestFlattenCaps(t *testing.T) {
	t.Run("too many layers", func(t *testing.T) {
		l1 := makeUncompressedLayer(t, []tarItem{{hdr: tar.Header{Name: "a"}, body: "1"}}, types.OCIUncompressedLayer)
		l2 := makeUncompressedLayer(t, []tarItem{{hdr: tar.Header{Name: "b"}, body: "2"}}, types.OCIUncompressedLayer)
		var out bytes.Buffer
		_, err := Flatten(makeImage(t, l1, l2), "test:layers", Limits{MaxLayers: 1}, &out)
		ie, ok := AsError(err)
		if !ok || ie.Code != CodeTooLarge {
			t.Fatalf("got err=%v, want %s", err, CodeTooLarge)
		}
	})

	t.Run("too many entries", func(t *testing.T) {
		l := makeUncompressedLayer(t, []tarItem{
			{hdr: tar.Header{Name: "a"}, body: "1"},
			{hdr: tar.Header{Name: "b"}, body: "2"},
			{hdr: tar.Header{Name: "c"}, body: "3"},
		}, types.OCIUncompressedLayer)
		var out bytes.Buffer
		_, err := Flatten(makeImage(t, l), "test:entries", Limits{MaxEntries: 2}, &out)
		ie, ok := AsError(err)
		if !ok || ie.Code != CodeTooLarge {
			t.Fatalf("got err=%v, want %s", err, CodeTooLarge)
		}
	})

	t.Run("decompression bomb stops early", func(t *testing.T) {
		big := strings.Repeat("0", 512<<10) // 512 KiB of zeros compresses to <1 KiB
		l := makeGzipLayer(t, []tarItem{{hdr: tar.Header{Name: "bomb"}, body: big}}, types.OCILayer)
		var out bytes.Buffer
		_, err := Flatten(makeImage(t, l), "test:bomb", Limits{MaxUncompressedBytes: 64 << 10}, &out)
		ie, ok := AsError(err)
		if !ok || ie.Code != CodeTooLarge {
			t.Fatalf("got err=%v, want %s", err, CodeTooLarge)
		}
	})

	t.Run("compressed size cap", func(t *testing.T) {
		rnd := make([]byte, 4096)
		if _, err := rand.Read(rnd); err != nil {
			t.Fatal(err)
		}
		l := makeGzipLayer(t, []tarItem{{hdr: tar.Header{Name: "a"}, body: string(rnd)}}, types.OCILayer)
		var out bytes.Buffer
		_, err := Flatten(makeImage(t, l), "test:comp", Limits{MaxCompressedBytes: 512}, &out)
		ie, ok := AsError(err)
		if !ok || ie.Code != CodeTooLarge {
			t.Fatalf("got err=%v, want %s", err, CodeTooLarge)
		}
	})
}

func FuzzFilterTar(f *testing.F) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "etc", Typeflag: tar.TypeDir, Mode: 0o755, Format: tar.FormatPAX})
	body := "127.0.0.1 localhost\n"
	_ = tw.WriteHeader(&tar.Header{Name: "etc/hosts", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)), Format: tar.FormatPAX})
	_, _ = io.WriteString(tw, body)
	_ = tw.Close()
	f.Add(buf.Bytes())
	f.Fuzz(func(_ *testing.T, data []byte) {
		var out bytes.Buffer
		_, _ = FilterTar(bytes.NewReader(data), &out, "fuzz:1", Limits{
			MaxUncompressedBytes: 1 << 20,
			MaxEntries:           1000,
		})
	})
}
