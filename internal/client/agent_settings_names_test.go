//go:build darwin || linux

package client

import (
	"context"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

func checkAgentReceiptNamesFixture(t *testing.T, original, receipt []string) (uint64, error) {
	t.Helper()
	conn, mode := agentCombinedInspectionFixture(t, nil)
	row := projectionRow()
	row["secrets"] = original
	// The opaque test field supplies a second source array. This test checks
	// only name-set equivalence; it creates no receipt or agent permission.
	row["policy_sources"] = map[string]any{"test_names": receipt}
	if err := os.WriteFile(sandboxStatePath(conn.Host), projectionState(t, 4, row, nil), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := openAgentSettingsSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := snapshot.close(); err != nil {
			t.Error(err)
		}
	}()
	err = withHostStateReadContext(t.Context(), conn.Host, func(owned context.Context) error {
		return withStateTransaction(owned, []string{"hosts", HostFileKey(conn.Host.Name)}, "sandboxes.json", false, false, func(tx *stateFileTxn) error {
			scope, err := newAgentMetadataScope(mode, snapshot, tx)
			if err != nil {
				return err
			}
			defer scope.close()
			return mode.inspector.engine.WithSources(scope.ctx, scope.descriptors(), func(h *tomlinspect.Inspection) error {
				defer scope.finishCachedWork()
				if err := h.InspectTOML(0, &mode.inspector.visitor); err != nil {
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
				if err != nil || !p.MatchesSource(ctx, source, source.Size()) {
					return tomlinspect.ErrIndexInvariant
				}
				_, err = projectAgentSandboxPolicyProjection(ctx, p, conn.Host.Name, projectionSession, func(row, _ api.JSONSpan, _ int, _ *agentSandboxPolicy) error {
					var original, receipt, opaque api.JSONSpan
					err := p.Object(row, []string{"secrets", "policy_sources"}, func(field api.JSONField) error {
						if field.Known == 0 {
							original = field.Value
						} else if field.Known == 1 {
							opaque = field.Value
						}
						return nil
					})
					if err != nil {
						return err
					}
					if err := p.Object(opaque, []string{"test_names"}, func(field api.JSONField) error {
						if field.Known == 0 {
							receipt = field.Value
						}
						return nil
					}); err != nil {
						return err
					}
					return checkAgentReceiptNames(ctx, p, row, original, receipt, h)
				})
				return err
			})
		})
	})
	return mode.inspector.engine.Stats().Pages, err
}

func TestAgentReceiptNamesMatchOriginalSortAndCompact(t *testing.T) {
	for _, test := range []struct {
		name              string
		original, receipt []string
		valid             bool
	}{
		{"ordered", []string{"A", "B"}, []string{"A", "B"}, true},
		{"unordered duplicates", []string{"B", "A", "A"}, []string{"A", "B"}, true},
		{"null original", nil, []string{}, true},
		{"empty original", []string{}, []string{}, true},
		{"missing original member", []string{"A"}, []string{"A", "B"}, false},
		{"extra original member", []string{"A", "B"}, []string{"A"}, false},
		{"duplicate receipt", []string{"A"}, []string{"A", "A"}, false},
		{"unordered receipt", []string{"A", "B"}, []string{"B", "A"}, false},
		{"null receipt", nil, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := checkAgentReceiptNamesFixture(t, test.original, test.receipt)
			if (err == nil) != test.valid {
				t.Fatalf("original-name set equality=%v; want valid=%v", err, test.valid)
			}
		})
	}
}

func TestAgentReceiptNamesReuseOneQueryRecord(t *testing.T) {
	var pages [2]uint64
	for index, count := range []int{1, 20_000} {
		t.Run([]string{"one", "many"}[index], func(t *testing.T) {
			original := make([]string, count)
			for i := range original {
				original[i] = "A"
			}
			var err error
			pages[index], err = checkAgentReceiptNamesFixture(t, original, []string{"A"})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	if pages[0] == 0 || pages[0] != pages[1] {
		t.Fatalf("repeated original names grew retained metadata pages: %v", pages)
	}
}
