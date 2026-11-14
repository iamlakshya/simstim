package egress

import "testing"

func TestSavedGrammarGridUsesPinnedCompilerFactsAndExactPositions(t *testing.T) {
	rules, err := pinnedResponseRuleSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules.rules {
		spec, err := inspectSavedGrammar(t.Context(), "KEY", rule.re.String())
		if err != nil {
			t.Fatal(err)
		}
		program, err := spec.compile("KEY")
		if err != nil {
			t.Fatal(err)
		}
		grid, err := savedGrammarGridFor(t.Context(), "KEY", program, spec.width.maximum)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("public rule=%s instructions=%d min_width=%d max_width=%d alternatives=%d grid_bits=%d grid_bytes=%d seen_bound=%d push_call_bound=%d frontier_bound=%d", rule.id, len(program.Inst), spec.width.minimum, spec.width.maximum, grid.alternatives, grid.states, (grid.states+7)/8, grid.seenBound, grid.pushBound, grid.frontierBound)
		for id := uint64(0); id < grid.states; id++ {
			position, ok := grid.position(id)
			if !ok {
				t.Fatal("representable exact grammar position was lost")
			}
			back, ok := grid.positionID(position)
			if !ok || back != id {
				t.Fatal("grammar position encoding merged distinct state flags or offsets")
			}
		}
		if _, ok := grid.position(grid.states); ok {
			t.Fatal("out-of-grid state became an accepted position")
		}
		if _, ok := grid.positionID(savedShapePosition{pc: uint32(len(program.Inst))}); ok { //nolint:gosec // G115: the same compiler limits the program to maxSentinelRegexWork instructions
			t.Fatal("out-of-program instruction became a grid bit")
		}
		if _, ok := grid.positionID(savedShapePosition{offset: -1}); ok {
			t.Fatal("negative grammar offset became a grid bit")
		}
		if _, ok := grid.positionID(savedShapePosition{offset: spec.width.maximum + 1}); ok {
			t.Fatal("past-end grammar offset became a grid bit")
		}
	}
}

func TestSavedGrammarGridKeepsExistingWorkLimitWithoutNewWidthCap(t *testing.T) {
	// This is a finite compiler fixture, not another accepted operational
	// saved-rule ID. Its width deliberately exceeds the current pinned rules.
	// Go's parser limits the product of nested repeat counts to 1000.
	// Adjacent finite repeats preserve this wider fixture without nesting.
	spec, err := inspectSavedGrammar(t.Context(), "KEY", `(p_[A-Z]{1000}[A-Z]{1000})`)
	if err != nil {
		t.Fatal(err)
	}
	program, err := spec.compile("KEY")
	if err != nil {
		t.Fatal(err)
	}
	grid, err := savedGrammarGridFor(t.Context(), "KEY", program, spec.width.maximum)
	if err != nil || grid.states <= uint64(maxSentinelRegexWork) || grid.seenBound != uint64(maxSentinelRegexWork) {
		t.Fatal("grid bounds reduced the existing finite grammar or work limit")
	}
	if grid.frontierBound != 1 || grid.pushBound != 2*uint64(maxSentinelRegexWork) {
		t.Fatal("nonbranching grammar acquired per-work frontier retention")
	}
	for _, id := range []uint64{0, grid.states / 2, grid.states - 1} {
		position, ok := grid.position(id)
		if !ok {
			t.Fatal("finite wider grammar lost a representable state")
		}
		if back, ok := grid.positionID(position); !ok || back != id {
			t.Fatal("wider grammar state did not round trip")
		}
	}
}
