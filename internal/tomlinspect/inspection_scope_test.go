//go:build darwin || linux

package tomlinspect

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fixtureMetadataSource struct {
	ctx     context.Context
	file    *os.File
	info    os.FileInfo
	kind    uint8
	revoked bool
}

func (s *fixtureMetadataSource) Kind() uint8 { return s.kind }
func (s *fixtureMetadataSource) Size() int64 { return s.info.Size() }
func (s *fixtureMetadataSource) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.revoked {
		return ErrIndexInvariant
	}
	info, err := s.file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(s.info, info) || info.Size() != s.info.Size() || !info.ModTime().Equal(s.info.ModTime()) {
		return ErrIndexInvariant
	}
	return nil
}
func (s *fixtureMetadataSource) ReadAt(p []byte, at int64) (int, error) {
	if err := s.Check(s.ctx); err != nil {
		return 0, err
	}
	return s.file.ReadAt(p, at)
}

type fixtureSourceFactory struct {
	*fixtureIndexFactory
	ctx     context.Context
	sources [2]MetadataSource
}

func (f *fixtureSourceFactory) AcquireSources(ctx context.Context, sources []MetadataSource) (IndexLease, error) {
	if ctx != f.ctx || len(sources) != 2 || sources[0] != f.sources[0] || sources[1] != f.sources[1] {
		return nil, ErrIndexInvariant
	}
	for _, s := range sources {
		if err := s.Check(ctx); err != nil {
			return nil, err
		}
	}
	return f.AcquireIndex(ctx, sources[0].Size())
}
func completeScopeFixture(t *testing.T) (*Inspector, *fixtureSourceFactory, string) {
	t.Helper()
	base := fixtureIndex(t)
	factory := &fixtureSourceFactory{fixtureIndexFactory: base, ctx: t.Context()}
	body := `{"name":"KEY","sentinel":"public","identity":"identity","prefix":"x","rule":"rule","host":"EXAMPLE.TEST","slot":"AB"}`
	for n, input := range []string{"a=1\n", body} {
		path := filepath.Join(base.root, []string{"settings", "state"}[n])
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		info, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		factory.sources[n] = &fixtureMetadataSource{ctx: t.Context(), file: file, info: info, kind: uint8(n + 1)}
	}
	inspector, err := NewInspector(factory)
	if err != nil {
		t.Fatal(err)
	}
	return inspector, factory, body
}
func scopeValueSpan(t *testing.T, body, value string) Span {
	t.Helper()
	token := `"` + value + `"`
	at := strings.Index(body, token)
	if at < 0 {
		t.Fatal("fixture lost source scalar")
	}
	return Span{Start: int64(at), End: int64(at + len(token))}
}
func TestCompleteInspectionOwnsRealSourcesRecordsTreesGridAndLifetime(t *testing.T) {
	inspector, factory, body := completeScopeFixture(t)
	var retained *Inspection
	err := inspector.WithSources(t.Context(), factory.sources[:], func(h *Inspection) error {
		retained = h
		if err := h.InspectTOML(0, syntaxOnlyVisitor{}); err != nil {
			return err
		}
		ctx, err := h.Context()
		if err != nil || ctx != t.Context() {
			return ErrIndexInvariant
		}
		source, err := h.Source(1)
		if err != nil || source != factory.sources[1] {
			return ErrIndexInvariant
		}
		span := Span{End: int64(len(body))}
		charge, err := h.CountState(1, span, func(c StateCounter) error {
			for _, f := range []func() error{c.Entry, c.Binding, c.Host, c.Slot} {
				if err := f(); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		region, err := h.BeginState(charge)
		if err != nil {
			return err
		}
		name := scopeValueSpan(t, body, "KEY")
		header := scopeValueSpan(t, body, "public")
		binding, err := region.AppendBinding(BindingRecord{Name: name, Header: header, Format: header})
		if err != nil {
			return err
		}
		slot, err := region.AppendSlot(SlotRecord{Kind: 1, Width: 1, Count: 2, ASCIISpan: scopeValueSpan(t, body, "AB")})
		if err != nil {
			return err
		}
		entry := SemanticEntry{Kind: 1, Armed: true, Name: name, Sentinel: header, Identity: scopeValueSpan(t, body, "identity"), Prefix: scopeValueSpan(t, body, "x"), Rule: scopeValueSpan(t, body, "rule"), BindingID: binding, SlotBase: slot, SlotCount: 1}
		first, err := region.AppendEntry(entry)
		if err != nil {
			return err
		}
		alias, err := region.AppendEntry(entry)
		if err != nil {
			return err
		}
		tree, err := region.NewTree(RelationSentinel, func(a, _, b, _ uint64) (int, error) {
			left, err := region.Entry(a)
			if err != nil {
				return 0, err
			}
			right, err := region.Entry(b)
			if err != nil {
				return 0, err
			}
			if left.Sentinel != right.Sentinel {
				return 0, ErrIndexInvariant
			}
			return 0, nil
		})
		if err != nil {
			return err
		}
		if inserted, err := tree.Put(first, 0, first); err != nil || !inserted {
			return errors.Join(err, ErrIndexInvariant)
		}
		if value, found, err := tree.Find(alias, 0); err != nil || !found || value != first {
			return errors.Join(err, ErrIndexInvariant)
		}
		ec, err := h.CountEntry(charge, span, func(c EntryCounter) error {
			if err := c.RunBoundary(); err != nil {
				return err
			}
			return c.Grammar(3, 5, 1, 4<<20)
		})
		if err != nil {
			return err
		}
		current, err := region.BeginEntry(ec)
		if err != nil {
			return err
		}
		runs, err := current.RunOffsets()
		if err != nil {
			return err
		}
		if _, err = runs.Put(0, 0, slot); err != nil {
			return err
		}
		grid, err := current.NewBitGrid(72)
		if err != nil {
			return err
		}
		if found, err := grid.Test(71); err != nil || found {
			return errors.Join(err, ErrIndexInvariant)
		}
		if err = grid.Set(71); err != nil {
			return err
		}
		if found, err := grid.Test(71); err != nil || !found {
			return errors.Join(err, ErrIndexInvariant)
		}
		frontier, err := current.Frontier()
		if err != nil {
			return err
		}
		for n := uint64(0); n < 9; n++ {
			if err = frontier.Push(n); err != nil {
				return err
			}
		}
		for n := uint64(9); n > 0; n-- {
			id, ok, err := frontier.Pop()
			if err != nil || !ok || id != n-1 {
				return errors.Join(err, ErrIndexInvariant)
			}
		}
		if _, ok, err := frontier.Pop(); err != nil || ok {
			return errors.Join(err, ErrIndexInvariant)
		}
		if err = current.Close(); err != nil {
			return err
		}
		if got, err := region.Entry(first); err != nil || got.BindingID != binding {
			return errors.Join(err, ErrIndexInvariant)
		}
		assertParsedTOMLPreserved(t, &inspector.store, inspector.parser.root.object)
		return region.Close()
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.opened != 1 || factory.closed != 1 {
		t.Fatal("complete source scope acquired another lease", factory.opened, factory.closed)
	}
	if _, err := retained.Source(1); err == nil {
		t.Fatal("inspection handle escaped its actual lifetime")
	}
	var tail [1]byte
	if n, err := factory.sources[1].ReadAt(tail[:], int64(len(body))); n != 0 || err != io.EOF {
		t.Fatal("index close closed the original source")
	}
}
func TestCompleteInspectionRejectsTokenReuseAndUnclosedRegions(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "unclosed", true: "reused"}[reuse], func(t *testing.T) {
			inspector, factory, body := completeScopeFixture(t)
			err := inspector.WithSources(t.Context(), factory.sources[:], func(h *Inspection) error {
				if err := h.InspectTOML(0, syntaxOnlyVisitor{}); err != nil {
					return err
				}
				charge, err := h.CountState(1, Span{End: int64(len(body))}, func(c StateCounter) error { return c.Entry() })
				if err != nil {
					return err
				}
				region, err := h.BeginState(charge)
				if err != nil {
					return err
				}
				if reuse {
					if err = region.Close(); err != nil {
						return err
					}
					_, _ = h.BeginState(charge)
				}
				return nil
			})
			if !errors.Is(err, ErrIndexInvariant) {
				t.Fatal("incomplete/reused metadata scope became success", err)
			}
			if factory.opened != factory.closed {
				t.Fatal("failed metadata scope retained lease")
			}
		})
	}
}
