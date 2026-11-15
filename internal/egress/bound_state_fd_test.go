//go:build darwin || linux

package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

type fdStateTestSource struct {
	ctx     context.Context
	file    *os.File
	info    os.FileInfo
	kind    uint8
	revoked bool
}

func (s *fdStateTestSource) Kind() uint8 { return s.kind }
func (s *fdStateTestSource) Size() int64 { return s.info.Size() }
func (s *fdStateTestSource) Check(ctx context.Context) error {
	if ctx != s.ctx || s.revoked {
		return tomlinspect.ErrIndexInvariant
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := s.file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(s.info, info) || info.Size() != s.info.Size() || !info.ModTime().Equal(s.info.ModTime()) || !info.Mode().IsRegular() {
		return tomlinspect.ErrIndexInvariant
	}
	return nil
}
func (s *fdStateTestSource) ReadAt(p []byte, at int64) (int, error) {
	if err := s.Check(s.ctx); err != nil {
		return 0, err
	}
	return s.file.ReadAt(p, at)
}

type fdStateTestFactory struct {
	root    string
	ctx     context.Context
	sources [2]tomlinspect.MetadataSource
	closed  bool
}
type fdStateTestLease struct {
	owner     *fdStateTestFactory
	file      *os.File
	workspace []byte
}

func (l *fdStateTestLease) File() tomlinspect.IndexFile { return l.file }
func (l *fdStateTestLease) Workspace() []byte           { return l.workspace }
func (l *fdStateTestLease) Close() error {
	l.owner.closed = true
	clear(l.workspace)
	return l.file.Close()
}
func (f *fdStateTestFactory) AcquireIndex(context.Context, int64) (tomlinspect.IndexLease, error) {
	return nil, tomlinspect.ErrIndexInvariant
}
func (f *fdStateTestFactory) AcquireSources(ctx context.Context, sources []tomlinspect.MetadataSource) (tomlinspect.IndexLease, error) {
	if ctx != f.ctx || len(sources) != 2 || sources[0] != f.sources[0] || sources[1] != f.sources[1] {
		return nil, tomlinspect.ErrIndexInvariant
	}
	file, err := os.CreateTemp(f.root, "index-")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(file.Name()); err != nil {
		_ = file.Close()
		return nil, err
	}
	return &fdStateTestLease{owner: f, file: file, workspace: make([]byte, tomlinspect.IndexWorkspaceBytes)}, nil
}

type fdStateSyntaxVisitor struct{}

func (fdStateSyntaxVisitor) EnterTable(tomlinspect.PathCursor, tomlinspect.TableCursor) error {
	return nil
}
func (fdStateSyntaxVisitor) LeaveTable(tomlinspect.PathCursor) error { return nil }
func (fdStateSyntaxVisitor) EnterArray(tomlinspect.PathCursor, tomlinspect.ArrayCursor) error {
	return nil
}
func (fdStateSyntaxVisitor) LeaveArray(tomlinspect.PathCursor) error { return nil }
func (fdStateSyntaxVisitor) Scalar(tomlinspect.PathCursor, tomlinspect.ScalarCursor) error {
	return nil
}
func (fdStateSyntaxVisitor) Finish() error { return nil }

func fdStateValidateFixture(t *testing.T, state *api.SessionSecretState, names []string, mutate func(*api.JSONProjection, *tomlinspect.Inspection, *fdStateTestSource, api.JSONSpan, api.JSONSpan) error) error {
	t.Helper()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return fdStateValidateRawFixture(t, encoded, names, mutate)
}

func fdStateValidateRawFixture(t *testing.T, encodedState []byte, names []string, mutate func(*api.JSONProjection, *tomlinspect.Inspection, *fdStateTestSource, api.JSONSpan, api.JSONSpan) error) error {
	t.Helper()
	root := t.TempDir()
	for _, item := range []struct{ key, leaf string }{{"HOME", "home"}, {"USERPROFILE", "profile"}, {"XDG_CONFIG_HOME", "config"}, {"XDG_STATE_HOME", "state"}, {"XDG_DATA_HOME", "data"}, {"XDG_CACHE_HOME", "cache"}, {"XDG_RUNTIME_DIR", "run"}, {"XDG_CONFIG_DIRS", "config-dirs"}, {"XDG_DATA_DIRS", "data-dirs"}} {
		path := filepath.Join(root, item.leaf)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(item.key, path)
	}
	encodedNames, err := json.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}
	encoded := append([]byte(`{"allowed":`), encodedNames...)
	encoded = append(encoded, []byte(`,"state":`)...)
	encoded = append(encoded, encodedState...)
	encoded = append(encoded, '}')

	factory := &fdStateTestFactory{root: root, ctx: t.Context()}
	for index, body := range []string{"a=1\n", string(encoded)} {
		path := filepath.Join(root, []string{"settings-input", "state-input"}[index])
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
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
		factory.sources[index] = &fdStateTestSource{ctx: t.Context(), file: file, info: info, kind: uint8(index + 1)}
	}
	inspector, err := tomlinspect.NewInspector(factory)
	if err != nil {
		t.Fatal(err)
	}
	err = inspector.WithSources(t.Context(), factory.sources[:], func(h *tomlinspect.Inspection) error {
		if err := h.InspectTOML(0, fdStateSyntaxVisitor{}); err != nil {
			return err
		}
		ctx, err := h.Context()
		if err != nil {
			return err
		}
		source, err := h.Source(1)
		if err != nil {
			return err
		}
		p, err := api.NewJSONProjection(ctx, source, source.Size())
		if err != nil {
			return err
		}
		whole, err := p.Root()
		if err != nil {
			return err
		}
		var namesSpan, stateSpan api.JSONSpan
		if err := p.Object(whole, []string{"allowed", "state"}, func(field api.JSONField) error {
			if field.Known == 0 {
				namesSpan = field.Value
			} else {
				stateSpan = field.Value
			}
			return nil
		}); err != nil {
			return err
		}
		if mutate != nil {
			return mutate(p, h, source.(*fdStateTestSource), stateSpan, namesSpan)
		}
		return NewPublicStateProjectionSchema().Validate(ctx, p, stateSpan, namesSpan, h)
	})
	if !factory.closed {
		t.Fatal("actual metadata lease was not closed")
	}
	return err
}

func TestFDPublicStateMatchesSelectedFullSemanticsAndEveryHistoryDomain(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*api.SessionSecretState)
	}{
		{"valid", func(*api.SessionSecretState) {}},
		{"mode mismatch", func(s *api.SessionSecretState) {
			p := s.Primary["KEY"]
			p.Mode = api.SecretModeExposed
			s.Primary["KEY"] = p
		}},
		{"missing binding", func(s *api.SessionSecretState) { delete(s.Intent.Binds, "KEY") }},
		{"wrong shape", func(s *api.SessionSecretState) { p := s.Primary["KEY"]; p.Sentinel = "stnd0000"; s.Primary["KEY"] = p }},
		{"noncanonical adjacent runs", func(s *api.SessionSecretState) {
			s.Primary["KEY"].Shape.Slots = []api.SecretShapeSlot{{Class: "ascii-upper", Count: 2, Width: 1}, {Class: "ascii-upper", Count: 2, Width: 1}, {Class: "ascii-digit", Count: 4, Width: 1}}
		}},
		{"unknown prefix", func(s *api.SessionSecretState) { s.Primary["KEY"].Shape.Prefix = "claimed" }},
		{"retired alias mismatch", func(s *api.SessionSecretState) {
			p := s.Retired["KEY"][0]
			p.Identity = s.Primary["KEY"].Identity
			s.Retired["KEY"][0] = p
		}},
		{"retired foreign", func(s *api.SessionSecretState) { s.Retired["FOREIGN"] = s.Retired["KEY"] }},
		{"empty minted foreign", func(s *api.SessionSecretState) { s.Minted = map[string][]api.PublicMintedSecretState{"FOREIGN": {}} }},
		{"future", func(s *api.SessionSecretState) { s.Version++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := localPublicStateFixture()
			test.change(state)
			_, full := CloneLocalSessionSecretState(state, map[string]bool{"KEY": true})
			fd := fdStateValidateFixture(t, state, []string{"KEY"}, nil)
			if (full == nil) != (fd == nil) {
				t.Fatalf("full/FD semantic disagreement: full=%v fd=%v", full, fd)
			}
		})
	}
}

func TestFDPublicStateKeepsCanonicalBindingAliasesAndIndependentIdentities(t *testing.T) {
	state := localPublicStateFixture()
	state.Intent.Modes["ALIAS"] = api.SecretModeBound
	state.Intent.Binds["ALIAS"] = api.SecretBind{Hosts: []string{" EXAMPLE.COM. ", "example.com:443"}, Header: "authorization", Format: "Bearer {value}"}
	state.Primary["ALIAS"] = state.Primary["KEY"]
	if _, err := CloneLocalSessionSecretState(state, map[string]bool{"ALIAS": true, "KEY": true}); err != nil {
		t.Fatal(err)
	}
	if err := fdStateValidateFixture(t, state, []string{"ALIAS", "KEY"}, nil); err != nil {
		t.Fatal("FD tightened canonical binding equivalence", err)
	}
	p := state.Primary["ALIAS"]
	p.Armed = !p.Armed
	state.Primary["ALIAS"] = p
	if err := fdStateValidateFixture(t, state, []string{"ALIAS", "KEY"}, nil); err == nil {
		t.Fatal("same primary identity gained conflicting arming")
	}
}

func TestFDPublicStateRefusesForeignProjectionChangedSourceAndRetiredInspection(t *testing.T) {
	state := localPublicStateFixture()
	for _, mode := range []string{"foreign projection", "revoked source", "wrong context"} {
		t.Run(mode, func(t *testing.T) {
			err := fdStateValidateFixture(t, state, []string{"KEY"}, func(p *api.JSONProjection, h *tomlinspect.Inspection, source *fdStateTestSource, state, names api.JSONSpan) error {
				ctx, err := h.Context()
				if err != nil {
					return err
				}
				if mode == "foreign projection" {
					p, err = api.NewJSONProjection(ctx, strings.NewReader("{}"), 2)
					if err != nil {
						return err
					}
				}
				if mode == "revoked source" {
					source.revoked = true
				}
				if mode == "wrong context" {
					other, cancel := context.WithCancel(ctx)
					cancel()
					return NewPublicStateProjectionSchema().Validate(other, p, state, names, h)
				}
				return NewPublicStateProjectionSchema().Validate(ctx, p, state, names, h)
			})
			if err == nil {
				t.Fatal("borrowed FD validator accepted foreign or revoked authority")
			}
		})
	}
	var retained *tomlinspect.Inspection
	var projection *api.JSONProjection
	var savedState, savedNames api.JSONSpan
	if err := fdStateValidateFixture(t, state, []string{"KEY"}, func(p *api.JSONProjection, h *tomlinspect.Inspection, _ *fdStateTestSource, state, names api.JSONSpan) error {
		retained, projection, savedState, savedNames = h, p, state, names
		ctx, err := h.Context()
		if err != nil {
			return err
		}
		return NewPublicStateProjectionSchema().Validate(ctx, p, state, names, h)
	}); err != nil {
		t.Fatal(err)
	}
	if err := NewPublicStateProjectionSchema().Validate(t.Context(), projection, savedState, savedNames, retained); err == nil {
		t.Fatal("retained projection adopted an expired inspection")
	}
	if err := NewPublicStateProjectionSchema().Validate(t.Context(), nil, api.JSONSpan{}, api.JSONSpan{}, nil); !errors.Is(err, tomlinspect.ErrIndexInvariant) {
		t.Fatal("missing actual inspection was accepted")
	}
}

func TestFDPublicStateUsesTheSamePinnedGrammarForMintedAuthorityAndAliasChecks(t *testing.T) {
	state := localPublicStateFixture()
	value := api.Secret("sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20))
	shape, rule, err := compileBoundShape(t.Context(), "KEY", value, "", sentinelPunctuationToken68)
	if err != nil {
		t.Fatal(err)
	}
	public, err := publicSecretShape(t.Context(), "KEY", shape, rule)
	if err != nil {
		t.Fatal(err)
	}
	state.Minted = map[string][]api.PublicMintedSecretState{"KEY": {{Sentinel: value, Shape: public, Identity: strings.Repeat("d", 32), Authority: "example.com:443", Rule: rule}}}
	for _, test := range []struct {
		name   string
		change func(*api.SessionSecretState)
	}{
		{"valid pinned path", func(*api.SessionSecretState) {}},
		{"unapproved authority", func(s *api.SessionSecretState) { s.Minted["KEY"][0].Authority = "other.example:443" }},
		{"outer rule mismatch", func(s *api.SessionSecretState) { s.Minted["KEY"][0].Rule = "unknown" }},
		{"narrowed source class", func(s *api.SessionSecretState) {
			for i, slot := range s.Minted["KEY"][0].Shape.Slots {
				if slot.Class == "ascii-upper" {
					slot.Class = "ascii"
					slot.ASCII = "A"
					s.Minted["KEY"][0].Shape.Slots[i] = slot
					break
				}
			}
		}},
		{"duplicate mint", func(s *api.SessionSecretState) { s.Minted["KEY"] = append(s.Minted["KEY"], s.Minted["KEY"][0]) }},
		{"wrong sample", func(s *api.SessionSecretState) { s.Minted["KEY"][0].Sentinel = "invalid" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := api.CloneSessionSecretState(state)
			test.change(candidate)
			_, full := CloneLocalSessionSecretState(candidate, map[string]bool{"KEY": true})
			fd := fdStateValidateFixture(t, candidate, []string{"KEY"}, nil)
			if test.name == "valid pinned path" && full != nil {
				t.Fatal("authored pinned fixture is invalid", full)
			}
			if (full == nil) != (fd == nil) {
				t.Fatalf("pinned full/FD predicate disagreement: full=%v fd=%v", full, fd)
			}
		})
	}
}

func TestFDPublicStateAcceptsSupportedUnorderedMapsWithoutMaterialisingThem(t *testing.T) {
	raw := []byte(`{"version":1,"intent":{"version":1,"modes":{"B":"exposed","A":"exposed"}},"generation":"` + strings.Repeat("c", 32) + `","primary":{"B":{"mode":"exposed","armed":false},"A":{"mode":"exposed","armed":false}}}`)
	var state api.SessionSecretState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if _, err := CloneLocalSessionSecretState(&state, map[string]bool{"A": true, "B": true}); err != nil {
		t.Fatal("unordered full fixture is not supported", err)
	}
	if err := fdStateValidateRawFixture(t, raw, []string{"A", "B"}, nil); err != nil {
		t.Fatal("supported source order changed FD semantics", err)
	}
}

func TestFDPublicStateLargeReverseMapsUseTheGroupedIndex(t *testing.T) {
	var modes, primary strings.Builder
	names := make([]string, 300)
	for i := range names {
		names[i] = fmt.Sprintf("KEY_%04d", i)
	}
	for i := len(names) - 1; i >= 0; i-- {
		if modes.Len() != 0 {
			modes.WriteByte(',')
			primary.WriteByte(',')
		}
		fmt.Fprintf(&modes, "%q:\"exposed\"", names[i])
		fmt.Fprintf(&primary, "%q:{\"mode\":\"exposed\",\"armed\":false}", names[i])
	}
	raw := []byte(`{"version":1,"intent":{"version":1,"modes":{` + modes.String() + `}},"generation":"` + strings.Repeat("c", 32) + `","primary":{` + primary.String() + `}}`)
	var full api.SessionSecretState
	if err := json.Unmarshal(raw, &full); err != nil {
		t.Fatal(err)
	}
	local := make(map[string]bool, len(names))
	for _, name := range names {
		local[name] = true
	}
	if _, err := CloneLocalSessionSecretState(&full, local); err != nil {
		t.Fatal("reverse-map full fixture unsupported", err)
	}
	if err := fdStateValidateRawFixture(t, raw, names, nil); err != nil {
		t.Fatal("reverse-map FD descriptor lookup failed", err)
	}
}

func TestFDPublicStateFutureClassificationMatchesFullDTOBeforeStickyCore(t *testing.T) {
	base, err := json.Marshal(localPublicStateFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		old, new []byte
	}{
		{"outer future", []byte(`"version":1`), []byte(`"future":true,"version":1`)},
		{"nested future", []byte(`"slots":`), []byte(`"extra":1,"slots":`)},
		{"future version", []byte(`"version":1`), []byte(`"version":2`)},
		{"mode wrong type", []byte(`"KEY":"bound"`), []byte(`"KEY":1`)},
		{"duplicate after known invalid mode", []byte(`"KEY":"bound"`), []byte(`"KEY":"invalid","KEY":"bound"`)},
		{"duplicate with invalid generation", []byte(`"KEY":"bound"`), []byte(`"KEY":"bound","KEY":"bound"`)},
		{"duplicate invalid name", []byte(`"KEY":"bound"`), []byte(`"1BAD":"exposed","1BAD":"exposed"`)},
		{"decoded duplicate key", []byte(`"KEY":"bound"`), []byte(`"K\u0045Y":"bound","KEY":"bound"`)},
		{"shape wrong type", []byte(`"width":1`), []byte(`"width":"1"`)},
		{"armed wrong type", []byte(`"armed":false`), []byte(`"armed":"false"`)},
		{"known invalid identity", []byte(`"identity":"` + strings.Repeat("a", 32) + `"`), []byte(`"identity":"invalid"`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := bytes.Replace(base, test.old, test.new, 1)
			if test.name == "duplicate with invalid generation" {
				raw = bytes.Replace(raw, []byte(strings.Repeat("c", 32)), []byte("invalid"), 1)
			}
			if bytes.Equal(raw, base) {
				t.Fatal("authored structural mutation was not applied")
			}
			var full api.SessionSecretState
			if err := json.Unmarshal(raw, &full); err != nil {
				t.Fatal(err)
			}
			future := !full.Supported()
			err := fdStateValidateRawFixture(t, raw, []string{"KEY"}, func(p *api.JSONProjection, h *tomlinspect.Inspection, _ *fdStateTestSource, state, names api.JSONSpan) error {
				ctx, err := h.Context()
				if err != nil {
					return err
				}
				failure := NewPublicStateProjectionSchema().Validate(ctx, p, state, names, h)
				if errors.Is(failure, ErrUnsupportedPublicState) != future {
					t.Fatalf("DTO support classification changed: fullfuture=%v fd=%v", future, failure)
				}
				if future {
					return nil
				} // An unrelated future receipt may be preserved.
				return failure
			})
			if future && err != nil {
				t.Fatal("future classification poisoned the actual inspection", err)
			}
			if !future && err == nil {
				t.Fatal("known semantic invalid state became future or success")
			}
		})
	}
}
