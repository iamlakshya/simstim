//go:build darwin || linux

package tomlinspect

import (
	"errors"
	"strings"
	"testing"
)

func TestNumericFloorKeepsGapsAcrossLeafAndInternalSplits(t *testing.T) {
	factory := fixtureIndex(t)
	lease, err := factory.AcquireIndex(t.Context(), MaxInput)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	src, err := newSource(t.Context(), strings.NewReader(strings.Repeat("x", MaxInput)), MaxInput)
	if err != nil {
		t.Fatal(err)
	}
	var store indexStore
	if err = store.reset(t.Context(), lease.File(), lease.Workspace(), src); err != nil {
		t.Fatal(err)
	}
	const count = 40000
	for n := 0; n < count; n++ {
		index := n * 29989 % count
		var value numericValue
		write64(value[:], 0, uint64(index+1))
		if err = store.put(treeArrays, numericKey{uint32(index*3 + 7), 0}, value); err != nil {
			t.Fatal(err)
		}
	}
	tree := store.treeView()
	var path treePath
	if _, err = tree.descend(treeArrays, numericKey{20000, 0}, &path); err != nil || path.n < 2 {
		t.Fatal("floor corpus did not split an internal branch", err)
	}
	for query := uint32(0); query < uint32(count*3+20); query++ {
		key, value, found, err := tree.floor(treeArrays, numericKey{query, 0})
		if err != nil {
			t.Fatal(err)
		}
		if query < 7 {
			if found {
				t.Fatal("before-first floor invented a run")
			}
			continue
		}
		index := min((int(query)-7)/3, count-1)
		if !found || key.first != uint32(index*3+7) || key.second != 0 || read64(value[:], 0) != uint64(index+1) {
			t.Fatal("numeric predecessor changed across a gap", query, key, found)
		}
	}
	if store.pins != 0 {
		t.Fatal("numeric floor retained a page pin")
	}
	tree.comparison = &fixtureNumericComparison{store: &store}
	if _, _, _, err := tree.floor(treeArrays, numericKey{}); !errors.Is(err, ErrIndexInvariant) {
		t.Fatal("source-comparator tree used numeric predecessor", err)
	}
}

func TestRunOffsetsPublicFloorUsesActualCurrentEntry(t *testing.T) {
	inspector, factory, body := completeScopeFixture(t)
	err := inspector.WithSources(t.Context(), factory.sources[:], func(h *Inspection) error {
		if err := h.InspectTOML(0, syntaxOnlyVisitor{}); err != nil {
			return err
		}
		span := Span{End: int64(len(body))}
		charge, err := h.CountState(1, span, func(c StateCounter) error {
			if err := c.Slot(); err != nil {
				return err
			}
			return c.Slot()
		})
		if err != nil {
			return err
		}
		state, err := h.BeginState(charge)
		if err != nil {
			return err
		}
		first, err := state.AppendSlot(SlotRecord{Kind: 1, Width: 1, Count: 3, ASCIISpan: scopeValueSpan(t, body, "AB")})
		if err != nil {
			return err
		}
		second, err := state.AppendSlot(SlotRecord{Kind: 1, Width: 1, Count: 2, ASCIISpan: scopeValueSpan(t, body, "AB")})
		if err != nil {
			return err
		}
		entryCharge, err := h.CountEntry(charge, span, func(c EntryCounter) error {
			if err := c.RunBoundary(); err != nil {
				return err
			}
			if err := c.RunBoundary(); err != nil {
				return err
			}
			return c.Grammar(3, 8, 1, 4<<20)
		})
		if err != nil {
			return err
		}
		entry, err := state.BeginEntry(entryCharge)
		if err != nil {
			return err
		}
		runs, err := entry.RunOffsets()
		if err != nil {
			return err
		}
		if _, err = runs.Put(2, 0, first); err != nil {
			return err
		}
		if _, err = runs.Put(5, 0, second); err != nil {
			return err
		}
		for _, test := range []struct {
			query, start, value uint64
			found               bool
		}{{0, 0, 0, false}, {2, 2, first, true}, {4, 2, first, true}, {5, 5, second, true}, {8, 5, second, true}} {
			start, value, found, err := runs.Floor(test.query)
			if err != nil {
				return err
			}
			if start != test.start || value != test.value || found != test.found {
				return ErrIndexInvariant
			}
		}
		if err = entry.Close(); err != nil {
			return err
		}
		return state.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
}
