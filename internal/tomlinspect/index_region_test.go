//go:build darwin || linux

package tomlinspect

import (
	"errors"
	"strings"
	"testing"
)

func parsedRegionFixture(t *testing.T) (*indexStore, *regionPages, IndexLease, uint32) {
	t.Helper()
	factory := fixtureIndex(t)
	input := "a=1\n"
	lease, err := factory.AcquireIndex(t.Context(), int64(len(input)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	src, err := newSource(t.Context(), strings.NewReader(input), int64(len(input)))
	if err != nil {
		t.Fatal(err)
	}
	store := new(indexStore)
	if err := store.reset(t.Context(), lease.File(), lease.Workspace(), src); err != nil {
		t.Fatal(err)
	}
	var stack lexStack
	if err := stack.reset(lease.Workspace()); err != nil {
		t.Fatal(err)
	}
	parser := metadataParser{lx: lex(src, &stack), store: store}
	root, err := parser.run()
	if err != nil {
		t.Fatal(err)
	}
	regions := new(regionPages)
	if err := regions.reset(&store.indexPages); err != nil {
		t.Fatal(err)
	}
	return store, regions, lease, root.object
}

func assertParsedTOMLPreserved(t *testing.T, store *indexStore, root uint32) {
	t.Helper()
	name, found, err := store.lookupLiteral("a")
	if err != nil || !found {
		t.Fatal("live TOML name was lost", found, err)
	}
	value, found, err := store.mapValue(root, name)
	if err != nil || !found {
		t.Fatal("live TOML map was lost", found, err)
	}
	scalar, err := nativeScalar(store.textSource, item{typ: value.token, val: value.span})
	if n, ok := scalar.Integer(); err != nil || !ok || n != 1 {
		t.Fatal("live native TOML value changed", err)
	}
}

func TestOwnedRegionsPreserveLiveTOMLAndGlobalPagesOnReuse(t *testing.T) {
	store, regions, lease, root := parsedRegionFixture(t)
	defer closeRegionFixture(t, lease)
	global, err := fixtureBeginRegion(t, regions, 1)
	if err != nil {
		t.Fatal(err)
	}
	globalID, data, slot, err := regions.allocate(global, pageRecordData, 0)
	if err != nil {
		t.Fatal(err)
	}
	write32(data, indexPageHeader, 0x01234567)
	store.release(slot, true)
	runs, err := fixtureBeginRegion(t, regions, 2)
	if err != nil {
		t.Fatal(err)
	}
	first, _, slot, err := regions.allocate(runs, pageRecordData, 1)
	if err != nil {
		t.Fatal(err)
	}
	store.release(slot, true)
	_, _, slot, err = regions.allocate(runs, pageRecordData, 1)
	if err != nil {
		t.Fatal(err)
	}
	store.release(slot, true)
	highWater := store.nextPage
	if err := regions.retire(runs); err != nil {
		t.Fatal(err)
	}
	if regions.freeCount != 2 {
		t.Fatal("retirement lost owned pages")
	}
	fresh, err := fixtureBeginRegion(t, regions, 2)
	if err != nil {
		t.Fatal(err)
	}
	reused, _, slot, err := regions.allocate(fresh, pageRecordData, 1)
	if err != nil {
		t.Fatal(err)
	}
	store.release(slot, true)
	if reused != first || store.nextPage != highWater {
		t.Fatal("phase reuse grew backing high water")
	}
	data, slot, err = regions.pin(global, globalID)
	if err != nil {
		t.Fatal(err)
	}
	if read32(data, indexPageHeader) != 0x01234567 {
		t.Fatal("retired another region's metadata")
	}
	store.release(slot, false)
	assertParsedTOMLPreserved(t, store, root)
	if _, _, err := regions.pin(runs, reused); !errors.Is(err, ErrIndexInvariant) {
		t.Fatal("old phase generation adopted a reused page", err)
	}
	if regions.failure == nil {
		t.Fatal("stale region reference did not poison complete inspection")
	}
}

func TestOwnedRegionRetirementRequiresPinAndCallbackQuiescence(t *testing.T) {
	for _, callback := range []bool{false, true} {
		t.Run(map[bool]string{false: "page", true: "callback"}[callback], func(t *testing.T) {
			store, regions, lease, _ := parsedRegionFixture(t)
			defer closeRegionFixture(t, lease)
			ref, err := fixtureBeginRegion(t, regions, 3)
			if err != nil {
				t.Fatal(err)
			}
			_, _, slot, err := regions.allocate(ref, pageRecordData, 2)
			if err != nil {
				t.Fatal(err)
			}
			if callback {
				store.release(slot, true)
				if err := regions.enter(ref); err != nil {
					t.Fatal(err)
				}
			}
			if err := regions.retire(ref); !errors.Is(err, ErrIndexInvariant) {
				t.Fatal("active region retired", err)
			}
			if callback {
				regions.leave(ref)
			} else {
				store.release(slot, true)
			}
			if regions.freeCount != 0 {
				t.Fatal("live phase pages entered free chain")
			}
		})
	}
}

type failedRegionWrites struct {
	IndexFile
	fail bool
}

func (f *failedRegionWrites) WriteAt(data []byte, offset int64) (int, error) {
	if f.fail {
		return 0, errFixtureRegionIO
	}
	return f.IndexFile.WriteAt(data, offset)
}

var errFixtureRegionIO = errors.New("injected region flush failure")

func TestOwnedRegionFlushFailurePoisonsBeforeFreePublication(t *testing.T) {
	store, regions, lease, root := parsedRegionFixture(t)
	defer closeRegionFixture(t, lease)
	ref, err := fixtureBeginRegion(t, regions, 3)
	if err != nil {
		t.Fatal(err)
	}
	_, _, slot, err := regions.allocate(ref, pageRecordData, 2)
	if err != nil {
		t.Fatal(err)
	}
	store.release(slot, true)
	fault := &failedRegionWrites{IndexFile: lease.File(), fail: true}
	store.file = fault
	if err := regions.retire(ref); !errors.Is(err, ErrIndexIO) {
		t.Fatal("retire flush error was lost", err)
	}
	if regions.freeHead != 0 || regions.freeCount != 0 {
		t.Fatal("partly flushed phase published reusable pages")
	}
	if _, err := fixtureBeginRegion(t, regions, 3); !errors.Is(err, ErrIndexIO) {
		t.Fatal("poisoned outer inspection resumed", err)
	}
	// The existing TOML object plane is still present; only an actual outer
	// success is denied. No second file/provider is adopted after failure.
	fault.fail = false
	assertParsedTOMLPreserved(t, store, root)
}

// Close during the test body, before testing cancels t.Context for Cleanup.
// The registered Cleanup remains an idempotent emergency close on fatal setup.
func closeRegionFixture(t *testing.T, lease IndexLease) {
	t.Helper()
	if err := lease.Close(); err != nil {
		t.Error(err)
	}
}

func fixtureBeginRegion(t *testing.T, r *regionPages, slot uint16) (regionRef, error) {
	t.Helper()
	ref, err := r.begin(slot)
	if err != nil {
		return ref, err
	}
	return ref, r.setCharge(ref, uint64(r.pages.maxPages))
}
