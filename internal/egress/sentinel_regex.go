package egress

import (
	"context"
	"regexp/syntax"
	"strings"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// These are analysis work bounds, not credential length/entropy thresholds.
// They exceed the reviewed finite formats. Refusal never widens a grammar.
const maxSentinelRegexWork = proto.MaxFrame
const maxSentinelRegexDepth = 1000

type sentinelRegexSize struct{ minimum, maximum, cost int }

func compileReviewedSentinel(ctx context.Context, name string, value api.Secret, prefix, pattern string) (sentinelShape, error) {
	if len(pattern) > proto.MaxFrame {
		return sentinelShape{}, sentinelFailure(name, "reviewed credential grammar is not representable")
	}
	expression, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return sentinelShape{}, sentinelFailure(name, "reviewed credential grammar is malformed or unsupported")
	}
	var captured *syntax.Regexp
	captures, visited := 0, 0
	_, err = inspectSentinelRegex(ctx, name, expression, 0, &visited, &captures, &captured)
	if err != nil {
		return sentinelShape{}, err
	}
	if captures != 1 || captured == nil {
		return sentinelShape{}, sentinelFailure(name, "reviewed grammar must have one credential capture")
	}
	visited, captures = 0, 0
	var nestedCapture *syntax.Regexp
	width, err := inspectSentinelRegex(ctx, name, captured, 0, &visited, &captures, &nestedCapture)
	if err != nil {
		return sentinelShape{}, err
	}
	if len(value) < width.minimum || len(value) > width.maximum {
		return sentinelShape{}, sentinelFailure(name, "value length does not match the reviewed credential grammar")
	}
	for i := 0; i < len(value); i++ {
		if i%128 == 0 {
			if err := sentinelContext(ctx, name); err != nil {
				return sentinelShape{}, err
			}
		}
		if value[i] >= 128 {
			return sentinelShape{}, sentinelFailure(name, "reviewed credential grammar supports ASCII only")
		}
	}
	program, err := syntax.Compile(captured.Simplify())
	if err != nil || len(program.Inst) > maxSentinelRegexWork {
		return sentinelShape{}, sentinelFailure(name, "reviewed grammar exceeds bounded format analysis")
	}
	alphabets, assertions, err := traceSentinelRegex(ctx, name, string(value), program)
	if err != nil {
		return sentinelShape{}, err
	}
	// Preserve only actual boundary requirements. Unconditionally fixing
	// edge wordness could make a short, otherwise variable class a singleton.
	_, firstBoundary, lastBoundary := sentinelCaptureEdges(expression)
	if firstBoundary {
		assertions[0] = true
	}
	if lastBoundary {
		assertions[len(value)] = true
	}
	for offset := range assertions {
		for _, position := range []int{offset - 1, offset} {
			if position < 0 || position >= len(alphabets) {
				continue
			}
			word := syntax.IsWordChar(rune(value[position]))
			var narrowed strings.Builder
			for _, candidate := range alphabets[position] {
				if syntax.IsWordChar(candidate) == word {
					narrowed.WriteRune(candidate)
				}
			}
			if narrowed.Len() == 0 {
				return sentinelShape{}, sentinelFailure(name, "reviewed boundary has no preserving token alphabet")
			}
			alphabets[position] = narrowed.String()
		}
	}
	shape := sentinelShape{Prefix: prefix}
	for _, alphabet := range alphabets[len(prefix):] {
		shape.Slots = appendSentinelRun(shape.Slots, alphabet, 1)
	}
	return shape, nil
}

func sentinelCaptureEdges(node *syntax.Regexp) (found, first, last bool) {
	if node.Op == syntax.OpCapture && node.Cap == 1 {
		return true, false, false
	}
	for index, child := range node.Sub {
		found, first, last = sentinelCaptureEdges(child)
		if !found {
			continue
		}
		if node.Op == syntax.OpConcat {
			for i := index - 1; i >= 0; i-- {
				zero, boundary := sentinelEdgeAssertion(node.Sub[i], true)
				first = first || boundary
				if !zero {
					break
				}
			}
			for i := index + 1; i < len(node.Sub); i++ {
				zero, boundary := sentinelEdgeAssertion(node.Sub[i], false)
				last = last || boundary
				if !zero {
					break
				}
			}
		}
		return found, first, last
	}
	return false, false, false
}

func sentinelEdgeAssertion(node *syntax.Regexp, before bool) (bool, bool) {
	switch node.Op {
	case syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return true, true
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText:
		return true, false
	case syntax.OpLiteral:
		return len(node.Rune) == 0, false
	case syntax.OpConcat:
		boundary := false
		for i := range len(node.Sub) {
			index := i
			if before {
				index = len(node.Sub) - 1 - i
			}
			zero, word := sentinelEdgeAssertion(node.Sub[index], before)
			boundary = boundary || word
			if !zero {
				return false, boundary
			}
		}
		return true, boundary
	case syntax.OpAlternate:
		nullable, boundary := false, false
		for _, child := range node.Sub {
			zero, word := sentinelEdgeAssertion(child, before)
			nullable, boundary = nullable || zero, boundary || word
		}
		return nullable, boundary
	case syntax.OpQuest:
		_, boundary := sentinelEdgeAssertion(node.Sub[0], before)
		return true, boundary
	case syntax.OpRepeat:
		zero, boundary := sentinelEdgeAssertion(node.Sub[0], before)
		return node.Min == 0 || zero, boundary
	default:
		return false, false
	}
}

func sentinelRegexAdd(a, b int) (int, bool) {
	if a < 0 || b < 0 || a > maxSentinelRegexWork-b {
		return 0, false
	}
	return a + b, true
}

func sentinelRegexMultiply(a, b int) (int, bool) {
	if a < 0 || b < 0 || b != 0 && a > maxSentinelRegexWork/b {
		return 0, false
	}
	return a * b, true
}

func inspectSentinelRegex(ctx context.Context, name string, node *syntax.Regexp, depth int, visited, captures *int, captured **syntax.Regexp) (sentinelRegexSize, error) {
	fail := func() (sentinelRegexSize, error) {
		return sentinelRegexSize{}, sentinelFailure(name, "reviewed grammar has unsupported or excessive finite syntax")
	}
	if err := sentinelContext(ctx, name); err != nil {
		return sentinelRegexSize{}, err
	}
	*visited = *visited + 1
	if depth > maxSentinelRegexDepth || *visited > maxSentinelRegexWork || node.Flags&syntax.FoldCase != 0 {
		return fail()
	}
	switch node.Op {
	case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
		return sentinelRegexSize{cost: 1}, nil
	case syntax.OpLiteral:
		for _, r := range node.Rune {
			if r < 0 || r >= 128 {
				return fail()
			}
		}
		return sentinelRegexSize{len(node.Rune), len(node.Rune), len(node.Rune) + 1}, nil
	case syntax.OpCharClass:
		for _, r := range node.Rune {
			if r < 0 || r >= 128 {
				return fail()
			}
		}
		return sentinelRegexSize{1, 1, 2}, nil
	case syntax.OpCapture:
		if node.Cap != 1 || len(node.Sub) != 1 {
			return fail()
		}
		*captures = *captures + 1
		*captured = node.Sub[0]
		return inspectSentinelRegex(ctx, name, node.Sub[0], depth+1, visited, captures, captured)
	case syntax.OpQuest, syntax.OpRepeat:
		if len(node.Sub) != 1 {
			return fail()
		}
		minimum, maximum := 0, 1
		if node.Op == syntax.OpRepeat {
			minimum, maximum = node.Min, node.Max
		}
		if minimum < 0 || maximum < minimum {
			return fail()
		}
		child, err := inspectSentinelRegex(ctx, name, node.Sub[0], depth+1, visited, captures, captured)
		if err != nil {
			return sentinelRegexSize{}, err
		}
		low, ok1 := sentinelRegexMultiply(child.minimum, minimum)
		high, ok2 := sentinelRegexMultiply(child.maximum, maximum)
		cost, ok3 := sentinelRegexMultiply(child.cost+2, maximum)
		if !ok1 || !ok2 || !ok3 {
			return fail()
		}
		cost, ok3 = sentinelRegexAdd(cost, 1)
		if !ok3 {
			return fail()
		}
		return sentinelRegexSize{low, high, cost}, nil
	case syntax.OpConcat, syntax.OpAlternate:
		result := sentinelRegexSize{cost: 1}
		first := true
		for _, childNode := range node.Sub {
			child, err := inspectSentinelRegex(ctx, name, childNode, depth+1, visited, captures, captured)
			if err != nil {
				return sentinelRegexSize{}, err
			}
			var ok bool
			result.cost, ok = sentinelRegexAdd(result.cost, child.cost+1)
			if !ok {
				return fail()
			}
			if node.Op == syntax.OpConcat {
				result.minimum, ok = sentinelRegexAdd(result.minimum, child.minimum)
				if !ok {
					return fail()
				}
				result.maximum, ok = sentinelRegexAdd(result.maximum, child.maximum)
				if !ok {
					return fail()
				}
			} else {
				if first || child.minimum < result.minimum {
					result.minimum = child.minimum
				}
				if child.maximum > result.maximum {
					result.maximum = child.maximum
				}
			}
			first = false
		}
		return result, nil
	default:
		// Star, Plus, AnyChar, case folding and unbounded syntax are never
		// approximated into a broader sentinel language.
		return fail()
	}
}
