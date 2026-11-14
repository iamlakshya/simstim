package tomlinspect

// StateRegion owns one actual known state's metadata until every relation
// check completes. The handle is unique and cannot adopt a later state.
type StateRegion struct {
	h      *Inspection
	id     uint64
	closed bool
}
type EntryRegion struct {
	state  *StateRegion
	id     uint64
	closed bool
}

func (h *Inspection) BeginState(token StateCharge) (*StateRegion, error) {
	i, err := h.check()
	if err != nil {
		return nil, err
	}
	p := i.outer.pendingState
	if !i.outer.tomlDone || i.outer.stateActive || i.outer.countActive || token.owner != i || token.epoch != h.epoch || token.id == 0 || token.id != p.id {
		return nil, i.scopeFailure(ErrIndexInvariant)
	}
	charge, err := statePhysicalCharge(p.counts)
	if err != nil {
		return nil, i.scopeFailure(err)
	}
	if err = i.installCharge(charge, 0, 0); err != nil {
		return nil, err
	}
	ref, err := i.outer.regions.begin(1)
	if err != nil {
		return nil, i.scopeFailure(err)
	}
	if err = i.outer.regions.setCharge(ref, charge); err != nil {
		return nil, i.scopeFailure(err)
	}
	i.outer.books[1] = regionBook{ref: ref}
	i.outer.stateID, i.outer.stateSource, i.outer.stateSpan, i.outer.stateCounts = token.id, p.source, p.span, p.counts
	i.outer.stateActive = true
	i.outer.pendingState = stateChargeData{}
	return &StateRegion{h: h, id: token.id}, nil
}
func (s *StateRegion) check() (*Inspector, error) {
	if s == nil || s.h == nil {
		return nil, ErrIndexInvariant
	}
	if s.closed {
		return nil, s.h.invalid()
	}
	i, err := s.h.check()
	if err != nil {
		return nil, err
	}
	if !i.outer.stateActive || i.outer.stateID != s.id {
		return nil, i.scopeFailure(ErrIndexInvariant)
	}
	if err = i.outer.regions.check(i.outer.books[1].ref); err != nil {
		return nil, i.scopeFailure(err)
	}
	return i, nil
}
func (s *StateRegion) Close() error {
	if s == nil || s.closed {
		return nil
	}
	i, err := s.check()
	if err != nil {
		return err
	}
	if i.outer.entryActive || i.outer.pendingEntry.id != 0 || i.outer.countActive {
		return i.scopeFailure(ErrIndexInvariant)
	}
	if err = i.outer.regions.retire(i.outer.books[1].ref); err != nil {
		return i.scopeFailure(err)
	}
	s.closed = true
	i.outer.books[1] = regionBook{}
	i.outer.stateActive = false
	i.outer.stateID = 0
	i.outer.stateCounts = stateRecordCounts{}
	i.outer.stateSpan = Span{}
	return nil
}
func (s *StateRegion) FinishState() error { return s.Close() }
func (s *StateRegion) BeginEntry(token EntryCharge) (*EntryRegion, error) {
	i, err := s.check()
	if err != nil {
		return nil, err
	}
	p := i.outer.pendingEntry
	if i.outer.entryActive || i.outer.countActive || token.owner != i || token.epoch != s.h.epoch || token.parent != s.id || token.id == 0 || token.id != p.id || p.parent != s.id {
		return nil, i.scopeFailure(ErrIndexInvariant)
	}
	charges, err := entryPhysicalCharge(p.counts)
	if err != nil {
		return nil, i.scopeFailure(err)
	}
	global, err := statePhysicalCharge(i.outer.stateCounts)
	if err != nil {
		return nil, i.scopeFailure(err)
	}
	if err = i.installCharge(global, charges.runs, charges.grammar); err != nil {
		return nil, err
	}
	for _, phase := range []struct {
		slot  uint16
		pages uint64
	}{{2, charges.runs}, {3, charges.grammar}} {
		ref, err := i.outer.regions.begin(phase.slot)
		if err != nil {
			return nil, i.scopeFailure(err)
		}
		if err = i.outer.regions.setCharge(ref, phase.pages); err != nil {
			return nil, i.scopeFailure(err)
		}
		i.outer.books[phase.slot] = regionBook{ref: ref}
	}
	i.outer.entryID = token.id
	i.outer.entryCounts = p.counts
	i.outer.gridOpened = false
	i.outer.frontierOpened = false
	i.outer.frontierLength = 0
	i.outer.entryActive = true
	i.outer.runsBegun = false
	i.outer.grammarBegun = false
	i.outer.pendingEntry = entryChargeData{}
	return &EntryRegion{state: s, id: token.id}, nil
}
func (e *EntryRegion) check() (*Inspector, error) {
	if e == nil || e.state == nil {
		return nil, ErrIndexInvariant
	}
	if e.closed {
		return nil, e.state.h.invalid()
	}
	i, err := e.state.check()
	if err != nil {
		return nil, err
	}
	if !i.outer.entryActive || i.outer.entryID != e.id {
		return nil, i.scopeFailure(ErrIndexInvariant)
	}
	return i, nil
}
func (e *EntryRegion) Close() error {
	if e == nil || e.closed {
		return nil
	}
	i, err := e.check()
	if err != nil {
		return err
	}
	for _, slot := range []uint16{3, 2} {
		if err = i.outer.regions.retire(i.outer.books[slot].ref); err != nil {
			return i.scopeFailure(err)
		}
		i.outer.books[slot] = regionBook{}
	}
	e.closed = true
	i.outer.entryActive = false
	i.outer.entryID = 0
	i.outer.entryCounts = entryRecordCounts{}
	return nil
}
func (e *EntryRegion) FinishEntry() error { return e.Close() }

// installCharge reserves the maximum simultaneous live regions, never a
// multiplier of historical source size or an address-space-max fallback.
// Failure for a correct legal source is a bound/implementation defect.
func (i *Inspector) installCharge(global, runs, grammar uint64) error {
	pages, err := chargeSum(i.outer.tomlBound, global, runs, grammar)
	if err != nil {
		return i.scopeFailure(err)
	}
	if pages > uint64(^uint32(0)-1) {
		return i.scopeFailure(ErrIndexInvariant)
	}
	i.outer.maxBound = max(i.outer.maxBound, pages)
	i.store.maxPages = uint32(i.outer.maxBound)
	return nil
}
