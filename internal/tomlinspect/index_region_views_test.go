//go:build darwin || linux

package tomlinspect

import (
	"errors"
	"testing"
)

func TestTaggedDirectoriesKeepEqualClassesInSeparateRegions(t *testing.T) {
	store, regions, lease, root := parsedRegionFixture(t)
	defer closeRegionFixture(t, lease)
	a, err := fixtureBeginRegion(t, regions, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := fixtureBeginRegion(t, regions, 2)
	if err != nil {
		t.Fatal(err)
	}
	var aRoot, bRoot uint32
	aDir, err := regions.directoryView(a, 0, &aRoot)
	if err != nil {
		t.Fatal(err)
	}
	bDir, err := regions.directoryView(b, 0, &bRoot)
	if err != nil {
		t.Fatal(err)
	}
	aPage, err := aDir.page(0, true)
	if err != nil {
		t.Fatal(err)
	}
	bPage, err := bDir.page(0, true)
	if err != nil {
		t.Fatal(err)
	}
	if aPage == bPage || aRoot == bRoot {
		t.Fatal("independent class directories merged")
	}
	assertParsedTOMLPreserved(t, store, root)
	bRoot = aRoot // Fault: a valid same-class root belongs to the OTHER phase.
	if _, err := bDir.page(0, false); !errors.Is(err, ErrIndexCorrupt) {
		t.Fatal("foreign same-class root was adopted", err)
	}
	if regions.failure == nil {
		t.Fatal("ownership failure did not poison outer scope")
	}
}

func TestTaggedTreesKeepLocalKeysIsolatedAndRefuseForeignRoots(t *testing.T) {
	store, regions, lease, root := parsedRegionFixture(t)
	defer closeRegionFixture(t, lease)
	a, err := fixtureBeginRegion(t, regions, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := fixtureBeginRegion(t, regions, 2)
	if err != nil {
		t.Fatal(err)
	}
	var aRoots, bRoots [treeCount]uint32
	aTree, err := regions.treeView(a, &aRoots)
	if err != nil {
		t.Fatal(err)
	}
	bTree, err := regions.treeView(b, &bRoots)
	if err != nil {
		t.Fatal(err)
	}
	var first, second numericValue
	write32(first[:], 0, 1)
	write32(second[:], 0, 2)
	key := numericKey{1, 1}
	if err := aTree.put(treeMaps, key, first); err != nil {
		t.Fatal(err)
	}
	if err := bTree.put(treeMaps, key, second); err != nil {
		t.Fatal(err)
	}
	for index, tree := range []*numericTrees{&aTree, &bTree} {
		value, found, err := tree.get(treeMaps, key)
		if err != nil || !found || read32(value[:], 0) != uint32(index+1) {
			t.Fatal("equal local key escaped its region", found, err)
		}
	}
	assertParsedTOMLPreserved(t, store, root)
	bRoots[treeMaps] = aRoots[treeMaps]
	if _, _, err := bTree.get(treeMaps, key); !errors.Is(err, ErrIndexCorrupt) {
		t.Fatal("foreign same-tree root was adopted", err)
	}
}
