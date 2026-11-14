package image

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/klauspost/compress/zstd"
)

type flatFixture struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Body string `json:"body"`
	Link string `json:"link"`
	Mode int64  `json:"mode"`
}

func flatTar(t testing.TB, entries ...flatFixture) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, e := range entries {
		typ := map[string]byte{"": tar.TypeReg, "directory": tar.TypeDir, "hardlink": tar.TypeLink, "symlink": tar.TypeSymlink, "fifo": tar.TypeFifo, "character": tar.TypeChar, "block": tar.TypeBlock}[e.Type]
		h := &tar.Header{Name: e.Name, Typeflag: typ, Mode: e.Mode, Linkname: e.Link, Format: tar.FormatPAX}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.Body))
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			if _, err := io.WriteString(w, e.Body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type flatMemory struct{ data []byte }

func (m *flatMemory) WriteAt(b []byte, offset int64) (int, error) {
	end := int(offset) + len(b)
	if end > len(m.data) {
		m.data = append(m.data, make([]byte, end-len(m.data))...)
	}
	return copy(m.data[int(offset):], b), nil
}

func (m *flatMemory) ReadAt(b []byte, offset int64) (int, error) {
	if offset >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(b, m.data[int(offset):])
	if n != len(b) {
		return n, io.EOF
	}
	return n, nil
}

type flatOutput struct {
	header tar.Header
	body   string
}

func flatResult(t testing.TB, data []byte) ([]string, map[string]flatOutput) {
	t.Helper()
	r := tar.NewReader(bytes.NewReader(data))
	var names []string
	entries := map[string]flatOutput{}
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return names, entries
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		entries[h.Name] = flatOutput{*h, string(body)}
	}
}

func flatInputs(data ...[]byte) ([]Layer, []io.Reader) {
	layers := make([]Layer, len(data))
	readers := make([]io.Reader, len(data))
	for i, b := range data {
		layers[i] = Layer{MediaType: "application/vnd.oci.image.layer.v1.tar", Size: int64(len(b))}
		readers[i] = bytes.NewReader(b)
	}
	return layers, readers
}

func TestFlattenFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/flatten.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture [][]flatFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	var blobs [][]byte
	for _, layer := range fixture {
		blobs = append(blobs, flatTar(t, layer...))
	}
	layers, readers := flatInputs(blobs...)
	var out bytes.Buffer
	devices, err := Flatten(context.Background(), layers, readers, &flatMemory{}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if devices != 1 {
		t.Fatalf("dropped devices = %d", devices)
	}
	names, entries := flatResult(t, out.Bytes())
	want := []string{".", "etc", "etc/keep", "fifo", "survivor", "symlink", "var", "var/new"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("entry order = %q, want %q", names, want)
	}
	if entries["survivor"].body != "linked bytes" || entries["var/new"].body != "same layer survives" || entries["etc/keep"].body != "preserved" {
		t.Fatalf("whiteouts lost surviving data: %#v", entries)
	}
	if entries["symlink"].header.Linkname != "../outside" || entries["symlink"].header.Typeflag != tar.TypeSymlink || entries["fifo"].header.Typeflag != tar.TypeFifo {
		t.Fatal("symlink or FIFO was not preserved")
	}
}

func TestFlattenLinksAndReplacements(t *testing.T) {
	first := flatTar(t, flatFixture{Name: "z", Body: "old"}, flatFixture{Name: "a", Type: "hardlink", Link: "z"})
	second := flatTar(t, flatFixture{Name: "z", Body: "new"}, flatFixture{Name: "b", Type: "hardlink", Link: "a"})
	layers, readers := flatInputs(first, second)
	var out bytes.Buffer
	if _, err := Flatten(context.Background(), layers, readers, &flatMemory{}, &out); err != nil {
		t.Fatal(err)
	}
	_, entries := flatResult(t, out.Bytes())
	if entries["a"].body != "old" || entries["b"].header.Typeflag != tar.TypeLink || entries["b"].header.Linkname != "a" || entries["z"].body != "new" {
		t.Fatalf("hardlinks did not retain the original inode: %#v", entries)
	}
}

func TestFlattenWhiteoutKeepsThisLayer(t *testing.T) {
	cases := []struct{ lower, upper []flatFixture }{
		{[]flatFixture{{Name: "file", Body: "lower"}}, []flatFixture{{Name: "file", Body: "upper"}, {Name: ".wh.file"}}},
		{[]flatFixture{{Name: "d/lower", Body: "lower"}}, []flatFixture{{Name: "d/upper", Body: "upper"}, {Name: ".wh.d"}}},
	}
	for _, tc := range cases {
		layers, readers := flatInputs(flatTar(t, tc.lower...), flatTar(t, tc.upper...))
		var out bytes.Buffer
		if _, err := Flatten(context.Background(), layers, readers, &flatMemory{}, &out); err != nil {
			t.Fatal(err)
		}
		_, entries := flatResult(t, out.Bytes())
		if len(entries) == 0 {
			t.Fatal("whiteout removed the upper layer")
		}
		found := false
		for _, entry := range entries {
			found = found || entry.body == "upper"
			if entry.body == "lower" {
				t.Fatal("whiteout kept a lower entry")
			}
		}
		if !found {
			t.Fatal("whiteout lost the upper entry")
		}
	}
}

func TestFlattenRejectsUnsafePathsAndTypes(t *testing.T) {
	cases := [][]flatFixture{
		{{Name: "../escape"}}, {{Name: "a/../b"}}, {{Name: strings.Repeat("x", 4097)}},
		{{Name: "link", Type: "hardlink", Link: "missing"}},
		{{Name: "d", Type: "directory"}, {Name: "link", Type: "hardlink", Link: "d"}},
		{{Name: "d", Type: "symlink", Link: "/tmp"}, {Name: "d/file", Body: "unsafe"}},
	}
	for _, entries := range cases {
		layers, readers := flatInputs(flatTar(t, entries...))
		_, err := Flatten(context.Background(), layers, readers, &flatMemory{}, io.Discard)
		assertFlatCode(t, err, "image_bad_layer")
	}
	if _, err := flatName("nul\x00path"); err == nil {
		t.Fatal("NUL name passed")
	}
	for _, raw := range []string{"./a", "/a", "///./a", "a//./b"} {
		if name, err := flatName(raw); err != nil || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "./") {
			t.Fatalf("normalise %q = %q, %v", raw, name, err)
		}
	}
}

func assertFlatCode(t testing.TB, err error, code string) {
	t.Helper()
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != code || pe.Hint == "" {
		t.Fatalf("error = %v, want %s with a hint", err, code)
	}
}

func TestFlattenMetadataAndDeterminism(t *testing.T) {
	var input bytes.Buffer
	w := tar.NewWriter(&input)
	when := time.Unix(1700000000, 123456789)
	h := &tar.Header{Name: "./file", Typeflag: tar.TypeReg, Size: 4, Mode: 0o6750, Uid: 17, Gid: 19, ModTime: when,
		AccessTime: when.Add(time.Hour), ChangeTime: when.Add(time.Minute), Uname: "discard", Gname: "discard", Format: tar.FormatPAX,
		PAXRecords: map[string]string{"SCHILY.xattr.user.test": "keep", "SCHILY.xattr.trusted.overlay.opaque": "drop", "comment": "drop"}}
	if err := w.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, "body"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var previous []byte
	for i := 0; i < 2; i++ {
		layers, readers := flatInputs(input.Bytes())
		var out bytes.Buffer
		if _, err := Flatten(context.Background(), layers, readers, &flatMemory{}, &out); err != nil {
			t.Fatal(err)
		}
		_, entries := flatResult(t, out.Bytes())
		got := entries["file"].header
		if got.Mode != h.Mode || got.Uid != 17 || got.Gid != 19 || !got.ModTime.Equal(when) || !got.AccessTime.IsZero() || !got.ChangeTime.IsZero() || got.Uname != "" || got.Gname != "" || got.PAXRecords["SCHILY.xattr.user.test"] != "keep" || got.PAXRecords["SCHILY.xattr.trusted.overlay.opaque"] != "" || got.PAXRecords["comment"] != "" {
			t.Fatalf("metadata changed: %#v", got)
		}
		if previous != nil && !bytes.Equal(previous, out.Bytes()) {
			t.Fatal("same layers did not produce identical tar bytes")
		}
		previous = append([]byte(nil), out.Bytes()...)
	}
}

func TestFlattenCompressedFormats(t *testing.T) {
	raw := flatTar(t, flatFixture{Name: "file", Body: "body"})
	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	if _, err := gw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	zw, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderCRC(true))
	if err != nil {
		t.Fatal(err)
	}
	defer zw.Close()
	zs := zw.EncodeAll(raw, nil)
	for media, data := range map[string][]byte{
		"application/vnd.oci.image.layer.v1.tar+gzip":       gz.Bytes(),
		"application/vnd.docker.image.rootfs.diff.tar.gzip": gz.Bytes(),
		"application/vnd.oci.image.layer.v1.tar+zstd":       zs,
	} {
		var out bytes.Buffer
		_, err := Flatten(context.Background(), []Layer{{MediaType: media, Size: int64(len(data))}}, []io.Reader{bytes.NewReader(data)}, &flatMemory{}, &out)
		if err != nil {
			t.Fatalf("%s: %v", media, err)
		}
		_, entries := flatResult(t, out.Bytes())
		if entries["file"].body != "body" {
			t.Fatal("compressed body was not preserved")
		}
	}
	corrupt := append([]byte(nil), gz.Bytes()...)
	corrupt[len(corrupt)-8] ^= 1
	_, err = Flatten(context.Background(), []Layer{{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Size: int64(len(corrupt))}}, []io.Reader{bytes.NewReader(corrupt)}, &flatMemory{}, io.Discard)
	assertFlatCode(t, err, "image_bad_layer")
	corrupt = append([]byte(nil), zs...)
	corrupt[len(corrupt)-1] ^= 1
	_, err = Flatten(context.Background(), []Layer{{MediaType: "application/vnd.oci.image.layer.v1.tar+zstd", Size: int64(len(corrupt))}}, []io.Reader{bytes.NewReader(corrupt)}, &flatMemory{}, io.Discard)
	assertFlatCode(t, err, "image_bad_layer")
}
