package egress

import (
	"context"
	"regexp/syntax"
	"strings"
)

type sentinelRegexPosition struct {
	pc     uint32
	offset int
}
type sentinelRegexParent struct {
	previous sentinelRegexPosition
	alphabet string
	boundary bool
}

// The program runs at offset0 and must finish at the value's actual end. It
// does not scan for tokens. A visited (instruction,offset) frontier bounds
// ambiguous finite alternatives instead of exponential recursive backtracking.
func traceSentinelRegex(ctx context.Context, name, value string, program *syntax.Prog) ([]string, map[int]bool, error) {
	start := sentinelRegexPosition{uint32(program.Start), 0} //nolint:gosec // G115: bounded syntax.Prog has non-negative Start
	stack := []sentinelRegexPosition{start}
	parents := map[sentinelRegexPosition]sentinelRegexParent{start: {}}
	alphabets := make(map[uint32]string)
	var accepted sentinelRegexPosition
	found := false
	push := func(next, previous sentinelRegexPosition, alphabet string, boundary bool) error {
		if _, seen := parents[next]; seen {
			return nil
		}
		if len(parents) >= maxSentinelRegexWork {
			return sentinelFailure(name, "reviewed grammar exhausted bounded format analysis")
		}
		parents[next] = sentinelRegexParent{previous, alphabet, boundary}
		stack = append(stack, next)
		return nil
	}
	for len(stack) != 0 {
		if err := sentinelContext(ctx, name); err != nil {
			return nil, nil, err
		}
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if int(current.pc) >= len(program.Inst) {
			return nil, nil, sentinelFailure(name, "reviewed grammar has an invalid format program")
		}
		instruction := &program.Inst[current.pc]
		next := sentinelRegexPosition{instruction.Out, current.offset}
		switch instruction.Op {
		case syntax.InstMatch:
			if current.offset == len(value) {
				accepted, found = current, true
			}
		case syntax.InstAlt, syntax.InstAltMatch:
			if err := push(sentinelRegexPosition{instruction.Arg, current.offset}, current, "", false); err != nil {
				return nil, nil, err
			}
			if err := push(next, current, "", false); err != nil {
				return nil, nil, err
			}
		case syntax.InstCapture, syntax.InstNop:
			if err := push(next, current, "", false); err != nil {
				return nil, nil, err
			}
		case syntax.InstEmptyWidth:
			before, after := rune(-1), rune(-1)
			if current.offset > 0 {
				before = rune(value[current.offset-1])
			}
			if current.offset < len(value) {
				after = rune(value[current.offset])
			}
			required := syntax.EmptyOp(instruction.Arg)
			if syntax.EmptyOpContext(before, after)&required == required {
				boundary := required&(syntax.EmptyWordBoundary|syntax.EmptyNoWordBoundary) != 0
				if err := push(next, current, "", boundary); err != nil {
					return nil, nil, err
				}
			}
		case syntax.InstRune, syntax.InstRune1:
			if current.offset == len(value) || !instruction.MatchRune(rune(value[current.offset])) {
				continue
			}
			alphabet := alphabets[current.pc]
			if alphabet == "" {
				var err error
				alphabet, err = sentinelInstructionAlphabet(name, instruction)
				if err != nil {
					return nil, nil, err
				}
				alphabets[current.pc] = alphabet
			}
			next.offset++
			if err := push(next, current, alphabet, false); err != nil {
				return nil, nil, err
			}
		case syntax.InstFail:
		default:
			return nil, nil, sentinelFailure(name, "reviewed grammar uses unsupported format instructions")
		}
		if found {
			break
		}
	}
	if !found {
		return nil, nil, sentinelFailure(name, "value does not match the reviewed credential grammar")
	}
	result := make([]string, len(value))
	assertions := make(map[int]bool)
	for current := accepted; current != start; {
		if err := sentinelContext(ctx, name); err != nil {
			return nil, nil, err
		}
		parent := parents[current]
		if parent.alphabet != "" {
			result[parent.previous.offset] = parent.alphabet
		}
		if parent.boundary {
			assertions[parent.previous.offset] = true
		}
		current = parent.previous
	}
	return result, assertions, nil
}

func sentinelInstructionAlphabet(name string, instruction *syntax.Inst) (string, error) {
	var out strings.Builder
	add := func(r rune) error {
		if r < 0 || r >= 128 || !sentinelRune(r) {
			return sentinelFailure(name, "reviewed credential alphabet is not a supported ASCII token class")
		}
		out.WriteRune(r)
		return nil
	}
	if len(instruction.Rune) == 1 {
		if err := add(instruction.Rune[0]); err != nil {
			return "", err
		}
	} else {
		for i := 0; i < len(instruction.Rune); i += 2 {
			for r := instruction.Rune[i]; r <= instruction.Rune[i+1]; r++ {
				if err := add(r); err != nil {
					return "", err
				}
			}
		}
	}
	if out.Len() == 0 {
		return "", sentinelFailure(name, "reviewed credential alphabet is empty")
	}
	return out.String(), nil
}
