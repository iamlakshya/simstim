//go:build darwin || linux

package tomlinspect

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestBorrowedOrderedTreeWalkUsesSemanticOrderAndReleasesCallbacks(t *testing.T) {
	factory := fixtureIndex(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	lease, err := factory.AcquireIndex(ctx, MaxInput)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	const count = 20000
	var input strings.Builder
	for n := range count {
		fmt.Fprintf(&input, "prefix_%06d\n", count-1-n)
	}
	body := input.String()
	src, err := newSource(ctx, strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	var store indexStore
	if err := store.reset(ctx, lease.File(), lease.Workspace(), src); err != nil {
		t.Fatal(err)
	}
	trees := store.treeView()
	trees.comparison = &fixtureTextComparison{store: &store}
	for n := range count {
		var raw [32]byte
		write32(raw[:], 0, uint32(n*14))
		write32(raw[:], 4, uint32(n*14+13))
		id, err := store.appendRecord(recordObjects, raw[:])
		if err != nil {
			t.Fatal(err)
		}
		var value numericValue
		write32(value[:], 0, uint32(n))
		if err := trees.put(treeMaps, numericKey{id, 0}, value); err != nil {
			t.Fatal(err)
		}
	}
	var path treePath
	if _, err := trees.descend(treeMaps, numericKey{count / 2, 0}, &path); err != nil || path.n < 2 {
		t.Fatal("ordered traversal fixture did not split an internal branch", err)
	}
	seen := 0
	err = trees.walkOrdered(treeMaps, func(key numericKey, value numericValue) error {
		if store.pins != 0 || int(read32(value[:], 0)) != count-1-seen || key.first != uint32(count-seen) {
			return ErrIndexCorrupt
		}
		seen++
		return nil
	})
	if err != nil || seen != count {
		t.Fatal("complete traversal used ID order or a fabricated comparison sentinel", seen, err)
	}
	failure := errors.New("stop ordered visitor")
	if err := trees.walkOrdered(treeMaps, func(numericKey, numericValue) error { return failure }); !errors.Is(err, failure) {
		t.Fatal("ordered visitor failure was lost", err)
	}
	if store.pins != 0 {
		t.Fatal("ordered visitor failure retained a pin")
	}
	cancel()
	if err := trees.walkOrdered(treeMaps, func(numericKey, numericValue) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal("ordered cached traversal ignored cancellation", err)
	}
}
