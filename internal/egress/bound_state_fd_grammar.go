package egress

import (
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/tomlinspect"
)

type fdSavedShapeWalk struct {
	grid     savedGrammarGrid
	seen     tomlinspect.BitGrid
	frontier tomlinspect.Frontier
	count    uint64
}

func (s *fdSavedShapeWalk) Push(position savedShapePosition) (bool, error) {
	id, ok := s.grid.positionID(position)
	if !ok {
		return false, tomlinspect.ErrIndexInvariant
	}
	present, err := s.seen.Test(id)
	if err != nil {
		return false, err
	}
	if present {
		return true, nil
	}
	if s.count >= maxSentinelRegexWork {
		return false, nil
	}
	if err := s.seen.Set(id); err != nil {
		return false, err
	}
	if err := s.frontier.Push(id); err != nil {
		return false, err
	}
	s.count++
	return true, nil
}
func (s *fdSavedShapeWalk) Pop() (savedShapePosition, bool, error) {
	id, found, err := s.frontier.Pop()
	if err != nil || !found {
		return savedShapePosition{}, found, err
	}
	position, ok := s.grid.position(id)
	if !ok {
		return position, false, tomlinspect.ErrIndexInvariant
	}
	return position, true, nil
}

func (s *fdPublicState) grammar(shape fdShape, entrySpan api.JSONSpan) error {
	pattern, err := secretRuleExpression(shape.metrics.rule)
	if err != nil {
		return err
	}
	if pattern == "" {
		token, err := s.h.CountEntry(s.charge, fdSpan(entrySpan), func(tomlinspect.EntryCounter) error { return nil })
		if err != nil {
			return err
		}
		entry, err := s.region.BeginEntry(token)
		if err != nil {
			return err
		}
		return entry.FinishEntry()
	}
	spec, err := inspectSavedGrammar(s.ctx, "receipt", pattern)
	if err != nil {
		return err
	}
	width := shape.metrics.metrics.bytes
	if width < spec.width.minimum || width > spec.width.maximum {
		return invalidPublicSessionState()
	}
	program, err := spec.compile("receipt")
	if err != nil {
		return err
	}
	grid, err := savedGrammarGridFor(s.ctx, "receipt", program, width)
	if err != nil {
		return err
	}
	token, err := s.h.CountEntry(s.charge, fdSpan(entrySpan), func(counter tomlinspect.EntryCounter) error {
		for index := uint64(0); index < shape.count; index++ {
			if err := counter.RunBoundary(); err != nil {
				return err
			}
		}
		return counter.Grammar(grid.instructions, grid.width, grid.alternatives, maxSentinelRegexWork)
	})
	if err != nil {
		return err
	}
	entry, err := s.region.BeginEntry(token)
	if err != nil {
		return err
	}
	defer entry.Close()
	runs, err := entry.RunOffsets()
	if err != nil {
		return err
	}
	offset := uint64(len(shape.prefixText))
	for index := uint64(0); index < shape.count; index++ {
		slotID := shape.base + index
		slot, err := s.region.Slot(slotID)
		if err != nil {
			return err
		}
		if slot.Width != 1 || slot.Count > ^uint64(0)-offset {
			return invalidPublicSessionState()
		}
		if _, err := runs.Put(offset, 0, slotID); err != nil {
			return err
		}
		offset += slot.Count
	}
	if offset != uint64(width) {
		return invalidPublicSessionState()
	}
	alphabet := func(at int) (string, error) {
		if at < 0 || at >= width {
			return "", tomlinspect.ErrIndexInvariant
		}
		if at < len(shape.prefixText) {
			b := shape.prefixText[at]
			if b >= 128 {
				return "", invalidPublicSessionState()
			}
			return string([]byte{b}), nil
		}
		start, slotID, found, err := runs.Floor(uint64(at))
		if err != nil {
			return "", err
		}
		if !found {
			return "", tomlinspect.ErrIndexInvariant
		}
		slot, err := s.region.Slot(slotID)
		if err != nil {
			return "", err
		}
		if slot.Width != 1 || slot.Count > ^uint64(0)-start || uint64(at) >= start+slot.Count {
			return "", tomlinspect.ErrIndexInvariant
		}
		class, err := s.class(slot)
		if err != nil {
			return "", err
		}
		if text := namedSavedClassAlphabet(class.kind); text != "" {
			return text, nil
		}
		if class.kind == savedClassASCII {
			return string(class.ascii[:class.used]), nil
		}
		return "", invalidPublicSessionState()
	}
	if width == 0 {
		return invalidPublicSessionState()
	}
	first, err := alphabet(0)
	if err != nil {
		return err
	}
	last, err := alphabet(width - 1)
	if err != nil {
		return err
	}
	if spec.firstBoundary && !savedUniformWordClass(first) || spec.lastBoundary && !savedUniformWordClass(last) {
		return invalidPublicSessionState()
	}
	seen, err := entry.NewBitGrid(grid.states)
	if err != nil {
		return err
	}
	frontier, err := entry.Frontier()
	if err != nil {
		return err
	}
	store := &fdSavedShapeWalk{grid: grid, seen: seen, frontier: frontier}
	if err := walkSavedShapeWith(s.ctx, "receipt", program, width, len(shape.prefixText), spec.firstBoundary, spec.lastBoundary, alphabet, store); err != nil {
		return err
	}
	return entry.FinishEntry()
}
