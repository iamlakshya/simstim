//go:build darwin || linux

package tomlinspect

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

var errFixtureCompare = errors.New("injected exact comparator failure")

// Test-only exact source strategy. Both IDs name fixed metadata spans;
// their ID order and a deliberately identical assistance word are irrelevant.
type fixtureTextComparison struct {
	store *indexStore
}

func (c *fixtureTextComparison) compare(a, b numericKey) (int, error) {
	var left, right [32]byte
	if err := c.store.readRecord(recordObjects, a.first, left[:]); err != nil {
		return 0, err
	}
	if err := c.store.readRecord(recordObjects, b.first, right[:]); err != nil {
		return 0, err
	}
	text := func(raw [32]byte) Text {
		return Text{source: c.store.textSource, span: Span{Start: int64(read32(raw[:], 0)), End: int64(read32(raw[:], 4))}, kind: itemRawString, generation: c.store.textSource.generation}
	}
	equal, _, bit, err := c.store.compareNames(nameInput{text: text(left)}, text(right))
	if err != nil || equal {
		return 0, err
	}
	if bit == 0 {
		return -1, nil
	}
	return 1, nil
}

func TestBorrowedTreeUsesExactSourceComparisonAcrossIDsAndSplits(t *testing.T) {
	factory := fixtureIndex(t)
	lease, err := factory.AcquireIndex(t.Context(), MaxInput)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	const count = 40000
	var input strings.Builder
	spans := make([]Span, count)
	for n := range count {
		spans[n].Start = int64(input.Len())
		fmt.Fprintf(&input, "prefix_%06d", count-1-n)
		spans[n].End = int64(input.Len())
		input.WriteByte(10)
	}
	text := input.String()
	src, err := newSource(t.Context(), strings.NewReader(text), int64(len(text)))
	if err != nil {
		t.Fatal(err)
	}
	var store indexStore
	if err := store.reset(t.Context(), lease.File(), lease.Workspace(), src); err != nil {
		t.Fatal(err)
	}
	comparison := fixtureTextComparison{store: &store}
	trees := store.treeView()
	trees.comparison = &comparison
	var ids []uint32
	for n, span := range spans {
		var raw [32]byte
		write32(raw[:], 0, uint32(span.Start))
		write32(raw[:], 4, uint32(span.End))
		id, err := store.appendRecord(recordObjects, raw[:])
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		var value numericValue
		write32(value[:], 0, uint32(n))
		if err := trees.put(treeMaps, numericKey{id, 0}, value); err != nil {
			t.Fatal(err)
		}
	}
	var path treePath
	if _, err := trees.descend(treeMaps, numericKey{ids[count/2], 0}, &path); err != nil || path.n < 2 {
		t.Fatal("exact comparison corpus did not split an internal branch", err)
	}
	for n := 0; n < count; n += 997 {
		var raw [32]byte
		write32(raw[:], 0, uint32(spans[n].Start))
		write32(raw[:], 4, uint32(spans[n].End))
		alias, err := store.appendRecord(recordObjects, raw[:])
		if err != nil {
			t.Fatal(err)
		}
		value, found, err := trees.get(treeMaps, numericKey{alias, 0})
		if err != nil || !found || read32(value[:], 0) != uint32(n) {
			t.Fatal("equal source text under a distinct ID was not found", found, err)
		}
		write32(value[:], 8, 0xfed123)
		if err := trees.put(treeMaps, numericKey{alias, 0}, value); err != nil {
			t.Fatal(err)
		}
		value, found, err = trees.get(treeMaps, numericKey{ids[n], 0})
		if err != nil || !found || read32(value[:], 8) != 0xfed123 {
			t.Fatal("exact equal-key upsert lost its final winner", found, err)
		}
	}
	if err := trees.walk(treeMaps, 0, func(numericKey, numericValue) error { return nil }); !errors.Is(err, ErrIndexInvariant) {
		t.Fatal("source-key tree used numeric range semantics")
	}
	if store.pins != 0 {
		t.Fatal("source comparison retained a cache pin")
	}
}

func TestBorrowedTreeComparatorFailureReleasesSplitPins(t *testing.T) {
	factory := fixtureIndex(t)
	lease, err := factory.AcquireIndex(t.Context(), MaxInput)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	input := strings.Repeat("x", MaxInput)
	src, err := newSource(t.Context(), strings.NewReader(input), MaxInput)
	if err != nil {
		t.Fatal(err)
	}
	var store indexStore
	if err := store.reset(t.Context(), lease.File(), lease.Workspace(), src); err != nil {
		t.Fatal(err)
	}
	comparison := fixtureNumericComparison{store: &store}
	trees := store.treeView()
	trees.comparison = &comparison
	for n := uint32(0); n < leafCapacity; n++ {
		if err := trees.put(treeMaps, numericKey{n, 0}, numericValue{}); err != nil {
			t.Fatal(err)
		}
	}
	comparison.fail = true
	if err := trees.put(treeMaps, numericKey{leafCapacity, 0}, numericValue{}); !errors.Is(err, errFixtureCompare) {
		t.Fatal("split comparator failure was lost", err)
	}
	if store.pins != 0 {
		t.Fatal("failed split retained an old or new page pin")
	}
}

type fixtureNumericComparison struct {
	store *indexStore
	fail  bool
}

func (c *fixtureNumericComparison) compare(a, b numericKey) (int, error) {
	if c.fail && c.store.pins >= 2 {
		return 0, errFixtureCompare
	}
	return compareKey(a, b), nil
}
