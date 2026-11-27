package image

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func flatHeaderTar(t testing.TB, h *tar.Header, body string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if err := w.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(w, body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// flatPAXTar writes raw extended records so hostile paths, reserved sparse
// keys and override values reach the reader. tar.Writer deliberately refuses
// to create those records when they conflict with its own Header fields.
func flatPAXTar(t testing.TB, records map[string]string, h *tar.Header, body string) []byte {
	t.Helper()
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var pax strings.Builder
	for _, key := range keys {
		suffix := " " + key + "=" + records[key] + "\n"
		size := len(suffix) + 1
		for len(strconv.Itoa(size))+len(suffix) != size {
			size = len(strconv.Itoa(size)) + len(suffix)
		}
		fmt.Fprintf(&pax, "%d%s", size, suffix)
	}
	prefix := flatHeaderTar(t, &tar.Header{Name: "PaxHeaders.0/entry", Typeflag: tar.TypeReg, Size: int64(pax.Len()), Format: tar.FormatUSTAR}, pax.String())
	prefix[156] = tar.TypeXHeader
	for i := 148; i < 156; i++ {
		prefix[i] = ' '
	}
	checksum := 0
	for _, b := range prefix[:512] {
		checksum += int(b)
	}
	copy(prefix[148:156], fmt.Sprintf("%06o\x00 ", checksum))
	prefix = prefix[:512+(pax.Len()+511)/512*512]
	return append(prefix, flatHeaderTar(t, h, body)...)
}

func TestFlattenMetadataRepresentableBounds(t *testing.T) {
	for _, seconds := range []int64{-0x80000000, -1, 0, 0x7fffffff, 0x80000000, 0x100000000, 0x37fffffff} {
		for _, id := range []int{0, 65536, 1<<32 - 1} {
			t.Run(fmt.Sprintf("%d/%d", seconds, id), func(t *testing.T) {
				when := time.Unix(seconds, 999999999)
				h := &tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0o7777, Uid: id, Gid: id, ModTime: when, Format: tar.FormatPAX}
				layers, readers := flatInputs(flatHeaderTar(t, h, ""))
				var out bytes.Buffer
				if _, err := Flatten(context.Background(), layers, readers, &flatMemory{}, &out); err != nil {
					t.Fatal(err)
				}
				_, entries := flatResult(t, out.Bytes())
				got := entries["file"].header
				if got.Uid != id || got.Gid != id || got.Mode != 0o7777 || !got.ModTime.Equal(when) {
					t.Fatalf("representable PAX metadata changed: %#v", got)
				}
			})
		}
	}
}

func TestFlattenRejectsUnrepresentableMetadata(t *testing.T) {
	for name, change := range map[string]func(*tar.Header){
		"negative uid": func(h *tar.Header) { h.Uid = -1 },
		"large uid":    func(h *tar.Header) { h.Uid = 1 << 32 },
		"negative gid": func(h *tar.Header) { h.Gid = -1 },
		"large gid":    func(h *tar.Header) { h.Gid = 1 << 32 },
		"mode type":    func(h *tar.Header) { h.Mode = 0o10000 | 0o644 },
		"mode cast":    func(h *tar.Header) { h.Mode = 1<<16 | 0o644 },
		"negative mode": func(h *tar.Header) {
			h.Mode = -1
			h.Format = tar.FormatGNU
		},
		"early time": func(h *tar.Header) { h.ModTime = time.Unix(-0x80000001, 0) },
		"late time":  func(h *tar.Header) { h.ModTime = time.Unix(0x380000000, 0) },
		"Go zero PAX time": func(h *tar.Header) {
			h.PAXRecords = map[string]string{"mtime": "-62135596800"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := &tar.Header{Name: "file", Typeflag: tar.TypeReg, Mode: 0o644, ModTime: time.Unix(0, 0), Format: tar.FormatPAX}
			change(h)
			input := flatHeaderTar(t, h, "")
			if name == "Go zero PAX time" {
				input = flatPAXTar(t, h.PAXRecords, h, "")
			}
			layers, readers := flatInputs(input)
			_, err := Flatten(context.Background(), layers, readers, &flatMemory{}, io.Discard)
			assertFlatCode(t, err, "image_bad_layer")
		})
	}
}

func TestFlattenPreservesConflictingHardlinkAndSymlinkHeaders(t *testing.T) {
	base := flatTar(t, flatFixture{Name: "z", Body: "body", Mode: 0o644}, flatFixture{Name: "a", Type: "hardlink", Link: "z", Mode: 0o600}, flatFixture{Name: "symlink", Type: "symlink", Link: "../target", Mode: 0o400})
	layers, readers := flatInputs(base)
	var out bytes.Buffer
	if _, err := Flatten(context.Background(), layers, readers, &flatMemory{}, &out); err != nil {
		t.Fatal(err)
	}
	_, entries := flatResult(t, out.Bytes())
	if entries["a"].header.Mode != 0o600 || entries["a"].body != "body" || entries["z"].header.Mode != 0o644 || entries["z"].header.Typeflag != tar.TypeLink || entries["z"].header.Linkname != "a" {
		t.Fatal("normalised hardlink path headers were changed")
	}
	if entries["symlink"].header.Mode != 0o400 || entries["symlink"].header.Linkname != "../target" {
		t.Fatal("normalised symlink header was changed")
	}
	// This checks PAX headers, not ext4 inode metadata. hcsshim materialises
	// one inode for a hardlink group and sets symlink permissions to 0777.
}

func TestFlattenPAXNamesAndLinks(t *testing.T) {
	for name, h := range map[string]*tar.Header{
		"PAX traversal": {Name: "safe", Typeflag: tar.TypeReg, PAXRecords: map[string]string{"path": "a/../escape"}},
		"PAX NUL path":  {Name: "safe", Typeflag: tar.TypeReg, PAXRecords: map[string]string{"path": "bad\x00path"}},
		"PAX NUL link":  {Name: "safe", Typeflag: tar.TypeSymlink, Linkname: "target", PAXRecords: map[string]string{"linkpath": "bad\x00target"}},
		"long path":     {Name: "safe", Typeflag: tar.TypeReg, PAXRecords: map[string]string{"path": strings.Repeat("x", 4097)}},
		"long symlink":  {Name: "safe", Typeflag: tar.TypeSymlink, Linkname: strings.Repeat("x", 4097)},
		"long hardlink": {Name: "safe", Typeflag: tar.TypeLink, Linkname: strings.Repeat("x", 4097)},
	} {
		t.Run(name, func(t *testing.T) {
			h.Mode, h.ModTime, h.Format = 0o644, time.Unix(0, 0), tar.FormatPAX
			input := flatHeaderTar(t, h, "")
			if len(h.PAXRecords) != 0 {
				input = flatPAXTar(t, h.PAXRecords, h, "")
			}
			layers, readers := flatInputs(input)
			_, err := Flatten(context.Background(), layers, readers, &flatMemory{}, io.Discard)
			assertFlatCode(t, err, "image_bad_layer")
		})
	}
}

func TestFlattenPAXSparseLogicalDataAndCap(t *testing.T) {
	input := flatPAXTar(t, map[string]string{
		"GNU.sparse.major": "0", "GNU.sparse.minor": "1", "GNU.sparse.numblocks": "1", "GNU.sparse.map": "4096,1", "GNU.sparse.size": "8192",
	}, &tar.Header{Name: "sparse", Typeflag: tar.TypeReg, Size: 1, Mode: 0o600, ModTime: time.Unix(0, 0), Format: tar.FormatPAX}, "X")
	layers, readers := flatInputs(input)
	var out bytes.Buffer
	limits := flattenLimits{64 << 10, 16 << 10, 10, 2}
	if _, err := flattenWithLimits(context.Background(), layers, readers, &flatMemory{}, &out, limits); err != nil {
		t.Fatal(err)
	}
	_, entries := flatResult(t, out.Bytes())
	body := entries["sparse"].body
	if len(body) != 8192 || body[4096] != 'X' || strings.Trim(body[:4096]+body[4097:], "\x00") != "" {
		t.Fatal("PAX sparse logical data was not expanded exactly")
	}
	for key := range entries["sparse"].header.PAXRecords {
		if strings.HasPrefix(key, "GNU.sparse.") {
			t.Fatal("sparse decoding instructions leaked into normalised output")
		}
	}
	layers, readers = flatInputs(input)
	limits.uncompressed = 4096
	_, err := flattenWithLimits(context.Background(), layers, readers, &flatMemory{}, io.Discard, limits)
	assertFlatCode(t, err, "image_too_large")
}
