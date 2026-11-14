package image

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestFlattenEntryAccountingAfterDeletion(t *testing.T) {
	old := flatTar(t, flatFixture{Name: "d/a", Body: "a"}, flatFixture{Name: "d/b", Body: "b"})
	for name, upper := range map[string][]flatFixture{
		"whiteout": {{Name: ".wh.d"}, {Name: "a"}, {Name: "b"}, {Name: "c"}},
		"opaque":   {{Name: "d/.wh..wh..opq"}, {Name: "d/a"}, {Name: "d/b"}},
		"replace":  {{Name: "d", Body: "file"}, {Name: "a"}, {Name: "b"}},
		"device":   {{Name: "d", Type: "block"}, {Name: "a"}, {Name: "b"}, {Name: "c"}},
	} {
		t.Run(name, func(t *testing.T) {
			layers, readers := flatInputs(old, flatTar(t, upper...))
			var out bytes.Buffer
			limits := flattenLimits{1 << 20, 1 << 20, 4, 2}
			if _, err := flattenWithLimits(context.Background(), layers, readers, &flatMemory{}, &out, limits); err != nil {
				t.Fatal(err)
			}
			names, _ := flatResult(t, out.Bytes())
			if len(names) != 4 {
				t.Fatalf("count after deletion = %d, want 4", len(names))
			}
		})
	}
}

func TestFlattenMaximumDepthName(t *testing.T) {
	name := strings.Repeat("a/", 2047) + "ab"
	if len(name) != 4096 {
		t.Fatal("fixture has the wrong path length")
	}
	layers, readers := flatInputs(flatTar(t, flatFixture{Name: name, Body: "deep"}))
	var out bytes.Buffer
	if _, err := Flatten(context.Background(), layers, readers, &flatMemory{}, &out); err != nil {
		t.Fatal(err)
	}
	names, entries := flatResult(t, out.Bytes())
	if len(names) != 2049 || entries[name].body != "deep" {
		t.Fatalf("4096-byte deep name lost entries: %d", len(names))
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] != "." && !strings.HasPrefix(names[i], names[i-1]+"/") {
			t.Fatalf("directory did not precede child at %d", i)
		}
	}
}

func TestFlattenHardlinkToSymlinkAndFIFO(t *testing.T) {
	layers, readers := flatInputs(flatTar(t,
		flatFixture{Name: "z-link", Type: "symlink", Link: "../target", Mode: 0o400},
		flatFixture{Name: "a-link", Type: "hardlink", Link: "z-link", Mode: 0o600},
		flatFixture{Name: "z-fifo", Type: "fifo", Mode: 0o640},
		flatFixture{Name: "a-fifo", Type: "hardlink", Link: "z-fifo", Mode: 0o660},
	))
	var out bytes.Buffer
	if _, err := Flatten(context.Background(), layers, readers, &flatMemory{}, &out); err != nil {
		t.Fatal(err)
	}
	_, entries := flatResult(t, out.Bytes())
	if entries["a-link"].header.Typeflag != tar.TypeSymlink || entries["a-link"].header.Linkname != "../target" || entries["z-link"].header.Typeflag != tar.TypeLink || entries["z-link"].header.Linkname != "a-link" {
		t.Fatal("hardlink to symlink was followed or lost")
	}
	if entries["a-fifo"].header.Typeflag != tar.TypeFifo || entries["z-fifo"].header.Typeflag != tar.TypeLink || entries["z-fifo"].header.Linkname != "a-fifo" {
		t.Fatal("hardlink to FIFO lost inode identity")
	}
}

func TestFlattenHardlinkHeaderMetadata(t *testing.T) {
	first := flatHeaderTar(t, &tar.Header{Name: "z", Typeflag: tar.TypeReg, Mode: 0o644, Uid: 17, Gid: 19, ModTime: time.Unix(10, 11), Format: tar.FormatPAX,
		PAXRecords: map[string]string{"SCHILY.xattr.user.note": "target"}}, "")
	second := flatHeaderTar(t, &tar.Header{Name: "a", Typeflag: tar.TypeLink, Linkname: "z", Mode: 0o600, Uid: 23, Gid: 29, ModTime: time.Unix(20, 21), Format: tar.FormatPAX,
		PAXRecords: map[string]string{"SCHILY.xattr.user.note": "link"}}, "")
	layers, readers := flatInputs(first, second)
	var out bytes.Buffer
	if _, err := Flatten(context.Background(), layers, readers, &flatMemory{}, &out); err != nil {
		t.Fatal(err)
	}
	_, entries := flatResult(t, out.Bytes())
	for name, want := range map[string]struct {
		uid, gid int
		mode     int64
		mtime    time.Time
		xattr    string
	}{
		"a": {23, 29, 0o600, time.Unix(20, 21), "link"},
		"z": {17, 19, 0o644, time.Unix(10, 11), "target"},
	} {
		h := entries[name].header
		if h.Uid != want.uid || h.Gid != want.gid || h.Mode != want.mode || !h.ModTime.Equal(want.mtime) || h.PAXRecords["SCHILY.xattr.user.note"] != want.xattr {
			t.Fatalf("path %q lost its original metadata: %#v", name, h)
		}
	}
}

func TestFlattenMalformedPAX(t *testing.T) {
	input := flatPAXTar(t, map[string]string{"path": "safe"}, &tar.Header{Name: "safe", Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0), Format: tar.FormatPAX}, "")
	// The extended record begins after the first 512-byte header. Its length
	// must parse and exactly delimit the record, even when the tar is valid.
	input[512] = 'x'
	layers, readers := flatInputs(input)
	_, err := Flatten(context.Background(), layers, readers, &flatMemory{}, io.Discard)
	assertFlatCode(t, err, "image_bad_layer")
}

func TestFlattenRepeatedOpaqueThenWhiteout(t *testing.T) {
	for _, keepUpper := range []bool{false, true} {
		upper := []flatFixture{{Name: "d/.wh..wh..opq"}, {Name: "d/.wh..wh..opq"}}
		if keepUpper {
			upper = append(upper, flatFixture{Name: "d/new", Body: "upper"})
		}
		upper = append(upper, flatFixture{Name: ".wh.d"}, flatFixture{Name: ".wh.d"})
		layers, readers := flatInputs(flatTar(t, flatFixture{Name: "d/old", Body: "lower"}), flatTar(t, upper...))
		var out bytes.Buffer
		if _, err := Flatten(context.Background(), layers, readers, &flatMemory{}, &out); err != nil {
			t.Fatal(err)
		}
		_, entries := flatResult(t, out.Bytes())
		_, dirExists := entries["d"]
		if dirExists != keepUpper || entries["d/old"].body != "" {
			t.Fatal("repeated whiteouts kept a lower directory or lost an upper parent")
		}
		if keepUpper && entries["d/new"].body != "upper" {
			t.Fatal("repeated whiteouts lost the upper file")
		}
	}
}

func TestFlattenWhiteoutDoesNotHideUnsupportedType(t *testing.T) {
	input := flatHeaderTar(t, &tar.Header{Name: ".wh.target", Typeflag: 'Z', ModTime: time.Unix(0, 0), Format: tar.FormatPAX}, "")
	layers, readers := flatInputs(input)
	_, err := Flatten(context.Background(), layers, readers, &flatMemory{}, io.Discard)
	assertFlatCode(t, err, "image_bad_layer")
}
