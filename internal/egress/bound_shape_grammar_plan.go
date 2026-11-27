package egress

import (
	"context"
	"regexp/syntax"
)

// savedGrammarSpec is compiled from the same pinned capture used by the full
// saved-shape validator. It adds no rule selection or acceptance policy.
type savedGrammarSpec struct {
	captured                    *syntax.Regexp
	width                       sentinelRegexSize
	firstBoundary, lastBoundary bool
}

func inspectSavedGrammar(ctx context.Context, name, pattern string) (savedGrammarSpec, error) {
	ast, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return savedGrammarSpec{}, sentinelFailure(name, "saved grammar is not supported")
	}
	captures, visited := 0, 0
	var captured *syntax.Regexp
	if _, err := inspectSentinelRegex(ctx, name, ast, 0, &visited, &captures, &captured); err != nil || captures != 1 || captured == nil {
		return savedGrammarSpec{}, sentinelFailure(name, "saved grammar has no supported credential path")
	}
	visited, captures = 0, 0
	var nested *syntax.Regexp
	width, err := inspectSentinelRegex(ctx, name, captured, 0, &visited, &captures, &nested)
	if err != nil {
		return savedGrammarSpec{}, err
	}
	_, first, last := sentinelCaptureEdges(ast)
	return savedGrammarSpec{captured: captured, width: width, firstBoundary: first, lastBoundary: last}, nil
}

func (s savedGrammarSpec) compile(name string) (*syntax.Prog, error) {
	program, err := syntax.Compile(s.captured.Simplify())
	if err != nil || len(program.Inst) > maxSentinelRegexWork {
		return nil, sentinelFailure(name, "saved grammar exceeds bounded format analysis")
	}
	return program, nil
}

// savedGrammarGrid describes exact position IDs, rather than storing a record
// for every attempted walk step. The caller still enforces the existing seen
// work limit in the existing DFS order. These bounds are not new input caps.
type savedGrammarGrid struct {
	instructions, width uint64
	states              uint64
	alternatives        uint64
	seenBound           uint64
	pushBound           uint64
	frontierBound       uint64
}

func savedGrammarGridFor(ctx context.Context, name string, program *syntax.Prog, width int) (savedGrammarGrid, error) {
	fail := func() (savedGrammarGrid, error) {
		return savedGrammarGrid{}, sentinelFailure(name, "saved grammar positions are not representable")
	}
	if program == nil || len(program.Inst) == 0 || width < 0 {
		return fail()
	}
	grid := savedGrammarGrid{instructions: uint64(len(program.Inst)), width: uint64(width)}
	if grid.width == ^uint64(0) || grid.instructions > ^uint64(0)/(grid.width+1)/4 {
		return fail()
	}
	grid.states = grid.instructions * (grid.width + 1) * 4
	for i := range program.Inst {
		if i%128 == 0 {
			if err := sentinelContext(ctx, name); err != nil {
				return savedGrammarGrid{}, err
			}
		}
		if program.Inst[i].Op == syntax.InstAlt || program.Inst[i].Op == syntax.InstAltMatch {
			grid.alternatives++
		}
	}
	grid.seenBound = min(grid.states, uint64(maxSentinelRegexWork))
	// Each popped position calls push at most twice. A position is enqueued
	// only once, so at most seenBound positions can ever be popped.
	grid.pushBound = 2 * grid.seenBound
	// A normal step pops one and pushes at most one. Only an alternative
	// can increase the frontier. There are at most four flag combinations
	// at each alternative instruction and offset, independent of DFS order.
	branchPositions := grid.alternatives * (grid.width + 1) * 4
	grid.frontierBound = min(grid.seenBound, branchPositions+1)
	return grid, nil
}

func (g savedGrammarGrid) positionID(position savedShapePosition) (uint64, bool) {
	if uint64(position.pc) >= g.instructions || position.offset < 0 || uint64(position.offset) > g.width {
		return 0, false
	}
	id := (uint64(position.pc)*(g.width+1) + uint64(position.offset)) * 4
	if position.boundary {
		id++
	}
	if position.need {
		id += 2
	}
	return id, id < g.states
}

func (g savedGrammarGrid) position(id uint64) (savedShapePosition, bool) {
	if id >= g.states || g.instructions > uint64(^uint32(0))+1 || g.width > uint64(int(^uint(0)>>1)) {
		return savedShapePosition{}, false
	}
	flags, cell := id%4, id/4
	return savedShapePosition{
		pc: uint32(cell / (g.width + 1)), offset: int(cell % (g.width + 1)), //nolint:gosec // G115: checked grid dimensions bound both conversions
		boundary: flags&1 != 0, need: flags&2 != 0,
	}, true
}
