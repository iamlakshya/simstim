package egress

import (
	"context"
	"regexp/syntax"
)

// Both selected typed state and borrowed FD state use this exact traversal,
// assertions and source-class predicate. Only position storage and alphabet
// lookup differ; the FD owner supplies its already charged numeric regions.
type savedShapeWalkStore interface {
	Push(savedShapePosition) (bool, error)
	Pop() (savedShapePosition, bool, error)
}

type typedSavedShapeWalk struct {
	frontier []savedShapePosition
	seen     map[savedShapePosition]bool
}

func (s *typedSavedShapeWalk) Push(p savedShapePosition) (bool, error) {
	if s.seen[p] {
		return true, nil
	}
	if len(s.seen) >= maxSentinelRegexWork {
		return false, nil
	}
	s.seen[p] = true
	s.frontier = append(s.frontier, p)
	return true, nil
}
func (s *typedSavedShapeWalk) Pop() (savedShapePosition, bool, error) {
	if len(s.frontier) == 0 {
		return savedShapePosition{}, false, nil
	}
	last := len(s.frontier) - 1
	p := s.frontier[last]
	s.frontier = s.frontier[:last]
	return p, true, nil
}

func savedShapeAssertionAt(alphabet func(int) (string, error), width, offset int, required syntax.EmptyOp) (bool, error) {
	var around [2]string
	count, position := 0, 0
	if offset > 0 {
		value, err := alphabet(offset - 1)
		if err != nil {
			return false, err
		}
		around[count] = value
		count++
		position++
	}
	if offset < width {
		value, err := alphabet(offset)
		if err != nil {
			return false, err
		}
		around[count] = value
		count++
	}
	return savedShapeAssertion(around[:count], position, required), nil
}

func walkSavedShapeWith(ctx context.Context, name string, program *syntax.Prog, width, prefix int, firstBoundary, lastBoundary bool, alphabet func(int) (string, error), store savedShapeWalkStore) error {
	push := func(position savedShapePosition) error {
		ok, err := store.Push(position)
		if err != nil {
			return err
		}
		if !ok {
			return sentinelFailure(name, "saved grammar exhausted bounded format analysis")
		}
		return nil
	}
	if err := push(savedShapePosition{pc: uint32(program.Start), boundary: firstBoundary}); err != nil {
		return err
	} //nolint:gosec // G115: the canonical finite compiler supplies a nonnegative Start
	sourceClasses := make(map[uint32]string)
	for {
		position, exists, err := store.Pop()
		if err != nil {
			return err
		}
		if !exists {
			break
		}
		if err := sentinelContext(ctx, name); err != nil {
			return err
		}
		if int(position.pc) >= len(program.Inst) {
			return sentinelFailure(name, "saved grammar instruction is invalid")
		}
		instruction := &program.Inst[position.pc]
		next := position
		next.pc = instruction.Out
		switch instruction.Op {
		case syntax.InstMatch:
			if position.offset == width && (!position.need || position.boundary || lastBoundary) {
				return nil
			}
			continue
		case syntax.InstAlt, syntax.InstAltMatch:
			other := position
			other.pc = instruction.Arg
			if err := push(other); err != nil {
				return err
			}
		case syntax.InstCapture, syntax.InstNop:
		case syntax.InstEmptyWidth:
			required := syntax.EmptyOp(instruction.Arg)
			matches, err := savedShapeAssertionAt(alphabet, width, position.offset, required)
			if err != nil {
				return err
			}
			if !matches {
				continue
			}
			if required&(syntax.EmptyWordBoundary|syntax.EmptyNoWordBoundary) != 0 {
				next.boundary = true
			}
		case syntax.InstRune, syntax.InstRune1:
			if position.offset == width || position.need && !position.boundary {
				continue
			}
			source := sourceClasses[position.pc]
			if source == "" {
				var err error
				source, err = sentinelInstructionAlphabet(name, instruction)
				if err != nil {
					return err
				}
				sourceClasses[position.pc] = source
			}
			provided, err := alphabet(position.offset)
			if err != nil {
				return err
			}
			narrowed := false
			if position.offset < prefix {
				if len(provided) != 1 || !instruction.MatchRune(rune(provided[0])) {
					continue
				}
			} else if provided != source {
				if provided != savedWordProjection(source, true) && provided != savedWordProjection(source, false) {
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
		if err := push(next); err != nil {
			return err
		}
	}
	return sentinelFailure(name, "supplied saved shape is not a pinned grammar path")
}
