//go:build darwin || linux

package tomlinspect

import (
	"errors"
	"testing"
)

func hostReferenceRegion(t *testing.T, h *Inspection, body string) (*StateRegion, uint64, uint64, uint64, HostRecord, error) {
	t.Helper()
	charge, err := h.CountState(1, Span{End: int64(len(body))}, func(c StateCounter) error {
		if err := c.Binding(); err != nil {
			return err
		}
		if err := c.Binding(); err != nil {
			return err
		}
		return c.Host()
	})
	if err != nil {
		return nil, 0, 0, 0, HostRecord{}, err
	}
	region, err := h.BeginState(charge)
	if err != nil {
		return nil, 0, 0, 0, HostRecord{}, err
	}
	group, err := region.AppendBinding(BindingRecord{Name: scopeValueSpan(t, body, "KEY"), FormatKind: 255})
	if err != nil {
		return region, 0, 0, 0, HostRecord{}, err
	}
	reference, err := region.AppendBinding(BindingRecord{Name: scopeValueSpan(t, body, "identity"), FormatKind: 1})
	if err != nil {
		return region, 0, 0, 0, HostRecord{}, err
	}
	key := scopeValueSpan(t, body, "EXAMPLE.TEST")
	value := HostRecord{BindingID: group, WholeSpan: key, InteriorSpan: key, ValueSpan: scopeValueSpan(t, body, "public")}
	id, err := region.AppendHost(value)
	return region, group, reference, id, value, err
}

func TestHostReferenceUpdatePreservesIndexedKeyAndSourceValue(t *testing.T) {
	inspector, factory, body := completeScopeFixture(t)
	var retained *StateRegion
	var retainedID uint64
	err := inspector.WithSources(t.Context(), factory.sources[:], func(h *Inspection) error {
		if err := h.InspectTOML(0, syntaxOnlyVisitor{}); err != nil {
			return err
		}
		region, group, reference, id, expected, err := hostReferenceRegion(t, h, body)
		if err != nil {
			return err
		}
		tree, err := region.NewTree(RelationHosts, func(_ uint64, a uint64, _ uint64, b uint64) (int, error) {
			x, err := region.Host(a)
			if err != nil {
				return 0, err
			}
			y, err := region.Host(b)
			if err != nil {
				return 0, err
			}
			if x.WholeSpan.Start < y.WholeSpan.Start {
				return -1, nil
			}
			if x.WholeSpan.Start > y.WholeSpan.Start {
				return 1, nil
			}
			return 0, nil
		})
		if err != nil {
			return err
		}
		if _, err := tree.Put(group, id, id); err != nil {
			return err
		}
		for _, ref := range []uint64{reference, 0} {
			if err := region.SetHostReference(id, ref); err != nil {
				return err
			}
			expected.ReferenceID = ref
			actual, err := region.Host(id)
			if err != nil {
				return err
			}
			if actual != expected {
				t.Fatal("a reference-only update changed the indexed key or independent source value")
			}
			foundID, found, err := tree.Find(group, id)
			if err != nil {
				return err
			}
			if !found || foundID != id {
				t.Fatal("a reference-only update changed exact indexed lookup")
			}
		}
		retained, retainedID = region, id
		return region.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := retained.SetHostReference(retainedID, 0); !errors.Is(err, ErrIndexInvariant) {
		t.Fatalf("a retired host handle adopted another region: %v", err)
	}
}

func TestHostReferenceRejectsInvalidSourceReferencesAndComparatorWrites(t *testing.T) {
	for _, kind := range []string{"append reference", "set reference", "host id", "value span", "comparator write"} {
		t.Run(kind, func(t *testing.T) {
			inspector, factory, body := completeScopeFixture(t)
			want := ErrIndexCorrupt
			if kind == "value span" || kind == "comparator write" {
				want = ErrIndexInvariant
			}
			err := inspector.WithSources(t.Context(), factory.sources[:], func(h *Inspection) error {
				if err := h.InspectTOML(0, syntaxOnlyVisitor{}); err != nil {
					return err
				}
				region, group, reference, id, value, err := hostReferenceRegion(t, h, body)
				if err != nil {
					return err
				}
				switch kind {
				case "append reference":
					value.ReferenceID = reference + 1
					_, err = region.AppendHost(value)
				case "set reference":
					err = region.SetHostReference(id, reference+1)
				case "host id":
					err = region.SetHostReference(0, reference)
				case "value span":
					value.ValueSpan.End = int64(len(body) + 1)
					_, err = region.AppendHost(value)
				case "comparator write":
					tree, failure := region.NewTree(RelationHosts, func(uint64, uint64, uint64, uint64) (int, error) {
						return 0, region.SetHostReference(id, reference)
					})
					if failure != nil {
						return failure
					}
					if _, err := tree.Put(group, id, id); err != nil {
						return err
					}
					value.WholeSpan = scopeValueSpan(t, body, "identity")
					value.InteriorSpan = value.WholeSpan
					other, failure := region.AppendHost(value)
					if failure != nil {
						return failure
					}
					_, err = tree.Put(group, other, other)
				}
				return err
			})
			if !errors.Is(err, want) {
				t.Fatalf("%s refused with %v; want %v", kind, err, want)
			}
		})
	}
}
