package egress

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func fullShapeMetricsOracle(t *testing.T, shape *api.SecretShape, policy sentinelPunctuationPolicy) (*sentinelPlan, error) {
	t.Helper()
	if shape == nil {
		return nil, sentinelFailure("receipt", "saved public token metadata is invalid")
	}
	decoded, err := legacyDecodePublicSecretShape(t.Context(), "KEY", shape, policy)
	if err != nil {
		return nil, err
	}
	return compileSentinelShape(t.Context(), "KEY", decoded)
}

func TestSymbolicShapeMetricsMatchUnchangedFullRunsAndControlBound(t *testing.T) {
	upper := api.SecretShapeSlot{Class: "ascii-upper", Width: 1, Count: 1}
	digit := api.SecretShapeSlot{Class: "ascii-digit", Width: 1, Count: proto.MaxFrame - 1024}
	for _, row := range []struct {
		shape  *api.SecretShape
		policy sentinelPunctuationPolicy
	}{
		{&api.SecretShape{Version: 1}, sentinelPunctuationPrintable},
		{&api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{}}, sentinelPunctuationPrintable},
		{&api.SecretShape{Version: 2, Slots: []api.SecretShapeSlot{upper}}, sentinelPunctuationPrintable},
		{&api.SecretShape{Version: 1, Prefix: "κ_", Slots: []api.SecretShapeSlot{upper}}, sentinelPunctuationPrintable},
		{&api.SecretShape{Version: 1, Prefix: "\xff", Slots: []api.SecretShapeSlot{upper}}, sentinelPunctuationPrintable},
		{&api.SecretShape{Version: 1, Prefix: "x\x00", Slots: []api.SecretShapeSlot{upper}}, sentinelPunctuationPrintable},
		{&api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{upper, upper}}, sentinelPunctuationPrintable},
		{&api.SecretShape{Version: 1, Prefix: strings.Repeat("p", 1024), Slots: []api.SecretShapeSlot{digit}}, sentinelPunctuationPrintable},
		{&api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{{Class: "ascii-digit", Width: 1, Count: proto.MaxFrame + 1}}}, sentinelPunctuationPrintable},
		{&api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{{Class: "ascii-upper", Width: 1, Count: 0}}}, sentinelPunctuationPrintable},
		{&api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{{Class: "ascii-upper", Width: 1, Count: -1}}}, sentinelPunctuationPrintable},
		{&api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{{Class: "unicode-upper", Width: 2, Count: 7}, upper}}, sentinelPunctuationPrintable},
		{&api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{{Class: "ascii-token68-punct", Width: 1, Count: 2}}}, sentinelPunctuationPrintable},
		{&api.SecretShape{Version: 1, Slots: []api.SecretShapeSlot{{Class: "ascii-token68-punct", Width: 1, Count: 2}}}, sentinelPunctuationToken68},
		// Nonempty rule bypasses only the generic-class predicate here.
		// Neither component oracle supplies the required pinned-path proof.
		{&api.SecretShape{Version: 1, Rule: "openai-api-key", Slots: []api.SecretShapeSlot{{Class: "ascii", Width: 1, Count: 2, ASCII: "AB"}, {Class: "ascii", Width: 1, Count: 2, ASCII: "BA"}}}, sentinelPunctuationUnset},
	} {
		full, fullErr := fullShapeMetricsOracle(t, row.shape, row.policy)
		symbolic, symbolicErr := symbolicPublicShapeMetrics(t.Context(), "KEY", row.shape, row.policy)
		if (fullErr == nil) != (symbolicErr == nil) {
			t.Fatal("symbolic shape acceptance differs from unchanged full canonical runs")
		}
		if fullErr == nil && (full.bytes != symbolic.bytes || full.variable != symbolic.variable || len(row.shape.Prefix) != symbolic.prefixBytes) {
			t.Fatal("symbolic shape changed exact byte width, variability or prefix size")
		}
	}
}

func TestSymbolicShapeLifecycleKeepsCancellationAndFailedPlansClosed(t *testing.T) {
	class, err := symbolicPublicClass(t.Context(), "KEY", api.SecretShapeSlot{Class: "ascii-upper", Width: 1})
	if err != nil {
		t.Fatal(err)
	}
	var state symbolicShapeValidator
	state.reset(t.Context(), "KEY")
	if err := state.prefixRune('κ'); err != nil {
		t.Fatal(err)
	}
	if err := state.slot(class, 2); err != nil {
		t.Fatal(err)
	}
	if metrics, err := state.finish(); err != nil || metrics.bytes != 4 || metrics.prefixBytes != 2 || !metrics.variable {
		t.Fatal("valid symbolic shape metrics changed")
	}
	if state.prefixRune('X') == nil {
		t.Fatal("finished shape accepted more source text")
	}
	if metrics, err := state.finish(); err == nil || metrics != (symbolicShapeMetrics{}) {
		t.Fatal("failed shape exposed a prior usable plan")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	state.reset(ctx, "KEY")
	if err := state.slot(class, 1); !errors.Is(err, context.Canceled) {
		t.Fatal("symbolic shape lost caller cancellation")
	}
	state.reset(t.Context(), "KEY")
	if err := state.slot(class, 1); err != nil || state.slot(class, 1) == nil {
		t.Fatal("adjacent equivalent source runs were not refused")
	}
	if fmt.Sprintf("%#v", state) != api.Redacted {
		t.Fatal("symbolic shape formatting exposed partial source metadata")
	}
}
