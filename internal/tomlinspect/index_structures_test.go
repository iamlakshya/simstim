//go:build darwin || linux

package tomlinspect

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNumericMetadataSplitsUpdatesAndRangeWalks(t *testing.T) {
	factory := fixtureIndex(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lease, err := factory.AcquireIndex(ctx, MaxInput)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	src, err := newSource(ctx, strings.NewReader(strings.Repeat("x", MaxInput)), MaxInput)
	if err != nil {
		t.Fatal(err)
	}
	var store indexStore
	if err := store.reset(ctx, lease.File(), lease.Workspace(), src); err != nil {
		t.Fatal(err)
	}
	const records = 40000
	for index := range records {
		key := numericKey{uint32(index%11 + 1), uint32(index) * 2654435761}
		var value numericValue
		write32(value[:], 0, uint32(index))
		write32(value[:], 4, ^uint32(index))
		if err := store.put(treeMaps, key, value); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < records; index += 997 {
		key := numericKey{uint32(index%11 + 1), uint32(index) * 2654435761}
		var value numericValue
		write32(value[:], 0, uint32(index))
		write32(value[:], 4, ^uint32(index))
		write32(value[:], 8, 0xfed123)
		if err := store.put(treeMaps, key, value); err != nil {
			t.Fatal(err)
		}
	}
	var path treePath
	if _, err := store.descend(treeMaps, numericKey{1, 0}, &path); err != nil {
		t.Fatal(err)
	}
	if path.n < 2 {
		t.Fatal("structure test did not split an internal branch")
	}
	for index := range records {
		key := numericKey{uint32(index%11 + 1), uint32(index) * 2654435761}
		value, found, err := store.get(treeMaps, key)
		if err != nil || !found || read32(value[:], 0) != uint32(index) || read32(value[:], 4) != ^uint32(index) {
			t.Fatal("numeric lookup changed metadata", index, found, err)
		}
		if index%997 == 0 && read32(value[:], 8) != 0xfed123 {
			t.Fatal("numeric upsert did not replace its metadata value")
		}
	}
	for group := uint32(1); group <= 11; group++ {
		count := 0
		if err := store.walk(treeMaps, group, func(key numericKey, value numericValue) error {
			index := read32(value[:], 0)
			if key.first != group || index%11+1 != group || key.second != index*2654435761 {
				return ErrIndexCorrupt
			}
			count++
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		want := 0
		for index := range records {
			if uint32(index%11+1) == group {
				want++
			}
		}
		if count != want {
			t.Fatal("numeric range crossed an object boundary")
		}
	}
	cancel()
	if _, _, err := store.get(treeMaps, numericKey{1, 0}); !errors.Is(err, context.Canceled) {
		t.Fatal("cached numeric lookup ignored cancellation")
	}
}

func TestExactNamesKeepEOFPrefixAndDecodedIdentity(t *testing.T) {
	factory := fixtureIndex(t)
	values := []string{strings.Repeat("a", 100000) + "x", strings.Repeat("a", 100000) + "y", "a", "", "ab", "abc", "\\u0061"}
	var sourceText strings.Builder
	spans := make([]Span, len(values))
	for index, value := range values {
		spans[index] = Span{Start: int64(sourceText.Len()), End: int64(sourceText.Len() + len(value))}
		sourceText.WriteString(value)
		sourceText.WriteByte('\n')
	}
	input := sourceText.String()
	lease, err := factory.AcquireIndex(t.Context(), int64(len(input)))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	src, err := newSource(t.Context(), strings.NewReader(input), int64(len(input)))
	if err != nil {
		t.Fatal(err)
	}
	var store indexStore
	if err := store.reset(t.Context(), lease.File(), lease.Workspace(), src); err != nil {
		t.Fatal(err)
	}
	ids := make([]uint32, len(values))
	for index, span := range spans {
		kind := itemRawString
		if index == len(values)-1 {
			kind = itemStringEsc
		}
		before := store.counters.TrieVisits
		ids[index], err = store.internName(Text{source: src, span: span, kind: kind})
		if err != nil {
			t.Fatal(err)
		}
		if index == 2 && store.counters.TrieVisits-before > 16 {
			t.Fatal("short name descended beyond its terminator into a long subtree")
		}
	}
	if ids[2] != ids[len(ids)-1] {
		t.Fatal("escaped and literal decoded keys received different exact identities")
	}
	for a := range ids {
		for b := 0; b < a; b++ {
			if a == len(ids)-1 && b == 2 {
				continue
			}
			if ids[a] == ids[b] {
				t.Fatal("different exact keys merged")
			}
		}
	}
	// No fingerprint is consulted by this backend. The predecessor's
	// forced-positive/collision corpus remains a native acceptance oracle;
	// this index's work follows exact bit positions and numeric IDs only.
}
