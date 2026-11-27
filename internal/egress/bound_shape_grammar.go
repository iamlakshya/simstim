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
	store := &typedSavedShapeWalk{seen: make(map[savedShapePosition]bool)}
	return walkSavedShapeWith(ctx, name, program, len(alphabets), prefix, firstBoundary, lastBoundary, func(offset int) (string, error) {
		if offset < 0 || offset >= len(alphabets) {
			return "", sentinelFailure(name, "saved grammar offset is invalid")
		}
		return alphabets[offset], nil
	}, store)
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
