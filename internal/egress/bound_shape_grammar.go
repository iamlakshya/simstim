package egress

import (
	"context"
	"regexp/syntax"
	"strings"
)

type savedShapePosition struct {
	pc             uint32
	offset         int
	boundary, need bool
}

// Validate the supplied source path, never a path selected from a random
// public sample. Two valid finite alternatives can match that same sample.
func validateSavedGrammarShape(ctx context.Context, name string, shape sentinelShape, pattern string) error {
	spec, err := inspectSavedGrammar(ctx, name, pattern)
	if err != nil {
		return err
	}
	plan, err := compileSentinelShape(ctx, name, shape)
	if err != nil || plan.bytes < spec.width.minimum || plan.bytes > spec.width.maximum {
		return sentinelFailure(name, "saved shape has no valid finite grammar width")
	}
	alphabets, err := savedShapeAlphabets(name, shape, plan.bytes)
	if err != nil {
		return err
	}
	if len(alphabets) == 0 {
		return sentinelFailure(name, "saved grammar path is empty")
	}
	program, err := spec.compile(name)
	if err != nil {
		return err
	}
	firstBoundary, lastBoundary := spec.firstBoundary, spec.lastBoundary
	if firstBoundary && !savedUniformWordClass(alphabets[0]) || lastBoundary && !savedUniformWordClass(alphabets[len(alphabets)-1]) {
		return sentinelFailure(name, "saved shape does not preserve outer grammar boundaries")
	}
	return walkSavedShape(ctx, name, program, alphabets, len(shape.Prefix), firstBoundary, lastBoundary)
}

func savedShapeAlphabets(name string, shape sentinelShape, size int) ([]string, error) {
	alphabets := make([]string, 0, size)
	for _, r := range shape.Prefix {
		if r >= 128 {
			return nil, sentinelFailure(name, "saved grammar prefix is not ASCII")
		}
		alphabets = append(alphabets, string(r))
	}
	for _, slot := range shape.Slots {
		for _, r := range slot.Alphabet {
			if r >= 128 {
				return nil, sentinelFailure(name, "saved grammar class is not ASCII")
			}
		}
		for range slot.Count {
			alphabets = append(alphabets, slot.Alphabet)
		}
	}
	return alphabets, nil
}

func walkSavedShape(ctx context.Context, name string, program *syntax.Prog, alphabets []string, prefix int, firstBoundary, lastBoundary bool) error {
	start := savedShapePosition{pc: uint32(program.Start), boundary: firstBoundary} //nolint:gosec // G115: validated finite program Start is nonnegative
	frontier := []savedShapePosition{start}
	seen := map[savedShapePosition]bool{start: true}
	sourceClasses := make(map[uint32]string)
	push := func(position savedShapePosition) bool {
		if seen[position] {
			return true
		}
		if len(seen) >= maxSentinelRegexWork {
			return false
		}
		seen[position] = true
		frontier = append(frontier, position)
		return true
	}
	for len(frontier) > 0 {
		if err := sentinelContext(ctx, name); err != nil {
			return err
		}
		position := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		if int(position.pc) >= len(program.Inst) {
			return sentinelFailure(name, "saved grammar instruction is invalid")
		}
		instruction := &program.Inst[position.pc]
		next := position
		next.pc = instruction.Out
		switch instruction.Op {
		case syntax.InstMatch:
			if position.offset == len(alphabets) && (!position.need || position.boundary || lastBoundary) {
				return nil
			}
			continue
		case syntax.InstAlt, syntax.InstAltMatch:
			other := position
			other.pc = instruction.Arg
			if !push(other) {
				return sentinelFailure(name, "saved grammar exhausted bounded format analysis")
			}
		case syntax.InstCapture, syntax.InstNop:
		case syntax.InstEmptyWidth:
			required := syntax.EmptyOp(instruction.Arg)
			if !savedShapeAssertion(alphabets, position.offset, required) {
				continue
			}
			if required&(syntax.EmptyWordBoundary|syntax.EmptyNoWordBoundary) != 0 {
				next.boundary = true
			}
		case syntax.InstRune, syntax.InstRune1:
			if position.offset == len(alphabets) || position.need && !position.boundary {
				continue
			}
			alphabet := sourceClasses[position.pc]
			if alphabet == "" {
				var err error
				alphabet, err = sentinelInstructionAlphabet(name, instruction)
				if err != nil {
					return err
				}
				sourceClasses[position.pc] = alphabet
			}
			provided := alphabets[position.offset]
			narrowed := false
			if position.offset < prefix {
				if len(provided) != 1 || !instruction.MatchRune(rune(provided[0])) {
					continue
				}
			} else if provided != alphabet {
				if provided != savedWordProjection(alphabet, true) && provided != savedWordProjection(alphabet, false) {
					continue
				}
				narrowed = true
			}
			next.offset++
			next.need = narrowed && !position.boundary
			next.boundary = false
		case syntax.InstFail:
			continue
		default:
			return sentinelFailure(name, "saved grammar instruction is unsupported")
		}
		if !push(next) {
			return sentinelFailure(name, "saved grammar exhausted bounded format analysis")
		}
	}
	return sentinelFailure(name, "supplied saved shape is not a pinned grammar path")
}

func savedWordProjection(alphabet string, word bool) string {
	var output strings.Builder
	for _, r := range alphabet {
		if syntax.IsWordChar(r) == word {
			output.WriteRune(r)
		}
	}
	return output.String()
}

func savedUniformWordClass(alphabet string) bool {
	return savedWordProjection(alphabet, true) == "" || savedWordProjection(alphabet, false) == ""
}

func savedShapeAssertion(alphabets []string, offset int, required syntax.EmptyOp) bool {
	before, after := []rune{-1}, []rune{-1}
	classes := func(alphabet string) []rune {
		var possibilities []rune
		if savedWordProjection(alphabet, true) != "" {
			possibilities = append(possibilities, 'A')
		}
		if savedWordProjection(alphabet, false) != "" {
			possibilities = append(possibilities, '-')
		}
		return possibilities
	}
	if offset > 0 {
		before = classes(alphabets[offset-1])
	}
	if offset < len(alphabets) {
		after = classes(alphabets[offset])
	}
	for _, left := range before {
		for _, right := range after {
			if syntax.EmptyOpContext(left, right)&required != required {
				return false
			}
		}
	}
	return true
}
