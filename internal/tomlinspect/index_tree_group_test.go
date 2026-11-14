//go:build darwin || linux

package tomlinspect

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The prefix is a numeric binding ID; the second word names an immutable
// source record. Second word zero is the lower-bound lookup sentinel.
type fixtureGroupedComparison struct{ text fixtureTextComparison }

func (c *fixtureGroupedComparison) compare(a, b numericKey) (int, error) {
	if a.first < b.first {
		return -1, nil
	}
	if a.first > b.first {
		return 1, nil
	}
	if a.second == 0 || b.second == 0 {
		if a.second == b.second {
			return 0, nil
		}
		if a.second == 0 {
			return -1, nil
		}
		return 1, nil
	}
	return c.text.compare(numericKey{a.second, 0}, numericKey{b.second, 0})
}

func TestBorrowedGroupedTreeWalkKeepsExactPrefixAndCrossBindingQueries(t *testing.T) {
	factory := fixtureIndex(t)
	lease, err := factory.AcquireIndex(t.Context(), MaxInput)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	const count = 1000
	var input strings.Builder
	spans := make([]Span, count)
	for n := range count {
		spans[n].Start = int64(input.Len())
		fmt.Fprintf(&input, "authority_%06d", count-1-n)
		spans[n].End = int64(input.Len())
		input.WriteByte(10)
	}
	body := input.String()
	src, err := newSource(t.Context(), strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var store indexStore
	if err := store.reset(t.Context(), lease.File(), lease.Workspace(), src); err != nil {
		t.Fatal(err)
	}
	comparison := fixtureGroupedComparison{text: fixtureTextComparison{store: &store}}
	trees := store.treeView()
	trees.comparison = &comparison
	trees.prefixGrouped = true
	for group := uint32(1); group <= 2; group++ {
		for n, span := range spans {
			var raw [32]byte
			write32(raw[:], 0, uint32(span.Start))
			write32(raw[:], 4, uint32(span.End))
			id, err := store.appendRecord(recordObjects, raw[:])
			if err != nil {
				t.Fatal(err)
			}
			var value numericValue
			write32(value[:], 0, uint32(n))
			if err := trees.put(treeMaps, numericKey{group, id}, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	for group := uint32(1); group <= 2; group++ {
		seen := 0
		err := trees.walk(treeMaps, group, func(key numericKey, value numericValue) error {
			if key.first != group || int(read32(value[:], 0)) != count-1-seen {
				return ErrIndexCorrupt
			}
			// Query the other set using THIS set's source reference. An ID-
			// based or hash-only equality fallback cannot find that member.
			other, found, err := trees.get(treeMaps, numericKey{3 - group, key.second})
			if err != nil {
				return err
			}
			if !found || read32(other[:], 0) != read32(value[:], 0) {
				return ErrIndexCorrupt
			}
			seen++
			return nil
		})
		if err != nil || seen != count {
			t.Fatal("grouped exact traversal or cross-set query changed", group, seen, err)
		}
	}
	failure := errors.New("stop grouped source visitor")
	if err := trees.walk(treeMaps, 1, func(numericKey, numericValue) error { return failure }); !errors.Is(err, failure) {
		t.Fatal("group callback error was lost", err)
	}
	if store.pins != 0 {
		t.Fatal("grouped lookup/walk retained a cache pin")
	}
}
