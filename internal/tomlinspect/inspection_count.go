package tomlinspect

// StateCounter is active ONLY during one SAME-source structural cursor pass.
// Each method counts one actually visited physical item, never a wire quota.
type StateCounter interface {
	Entry() error
	Binding() error
	Host() error
	Slot() error
}
type EntryCounter interface {
	RunBoundary() error
	Grammar(instructions, width, alternatives, work uint64) error
}

type stateCounter struct {
	h      *Inspection
	id     uint64
	active bool
	counts stateRecordCounts
}

func (c *stateCounter) increment(value *uint64) error {
	i, err := c.h.check()
	if err != nil {
		return err
	}
	if !c.active || !i.outer.countActive || i.outer.sequence != c.id {
		return i.scopeFailure(ErrIndexInvariant)
	}
	next, err := chargeAdd(*value, 1)
	if err != nil {
		return i.scopeFailure(err)
	}
	*value = next
	return nil
}
func (c *stateCounter) Entry() error   { return c.increment(&c.counts.entries) }
func (c *stateCounter) Binding() error { return c.increment(&c.counts.bindings) }
func (c *stateCounter) Host() error    { return c.increment(&c.counts.hosts) }
func (c *stateCounter) Slot() error    { return c.increment(&c.counts.slots) }

func (h *Inspection) CountState(source int, span Span, visit func(StateCounter) error) (_ StateCharge, err error) {
	i, err := h.check()
	if err != nil {
		return StateCharge{}, err
	}
	if !i.outer.tomlDone || i.outer.object.active || i.outer.object.pending.id != 0 || source != 1 || span.End <= span.Start || visit == nil || i.outer.stateActive || i.outer.entryActive || i.outer.countActive || i.outer.pendingState.id != 0 {
		return StateCharge{}, i.scopeFailure(ErrIndexInvariant)
	}
	if err := h.CheckSpan(source, span); err != nil {
		return StateCharge{}, err
	}
	if err := i.outer.sources[source].Check(i.outer.ctx); err != nil {
		return StateCharge{}, i.scopeFailure(err)
	}
	if i.outer.sequence == ^uint64(0) {
		return StateCharge{}, i.scopeFailure(ErrIndexInvariant)
	}
	i.outer.sequence++
	id := i.outer.sequence
	i.outer.countActive = true
	counter := stateCounter{h: h, id: id, active: true}
	defer func() {
		counter.active = false
		i.outer.countActive = false
		if err != nil {
			i.scopeFailure(err)
		}
	}()
	if err = visit(&counter); err != nil {
		return StateCharge{}, err
	}
	if err = i.outer.sources[source].Check(i.outer.ctx); err != nil {
		return StateCharge{}, err
	}
	// Each layout includes one attempted current candidate exactly once.
	for _, value := range []*uint64{&counter.counts.entries, &counter.counts.bindings, &counter.counts.hosts, &counter.counts.slots} {
		*value, err = chargeAdd(*value, 1)
		if err != nil {
			return StateCharge{}, err
		}
	}
	if _, err = statePhysicalCharge(counter.counts); err != nil {
		return StateCharge{}, err
	}
	i.outer.pendingState = stateChargeData{id: id, source: source, span: span, counts: counter.counts}
	return StateCharge{owner: i, epoch: h.epoch, id: id}, nil
}

type entryCounter struct {
	h               *Inspection
	id              uint64
	active, grammar bool
	runs            uint64
	grid            grammarPositionCounts
}

func (c *entryCounter) check() (*Inspector, error) {
	i, err := c.h.check()
	if err != nil {
		return nil, err
	}
	if !c.active || !i.outer.countActive || i.outer.sequence != c.id {
		return nil, i.scopeFailure(ErrIndexInvariant)
	}
	return i, nil
}
func (c *entryCounter) RunBoundary() error {
	i, err := c.check()
	if err != nil {
		return err
	}
	c.runs, err = chargeAdd(c.runs, 1)
	if err != nil {
		return i.scopeFailure(err)
	}
	return nil
}
func (c *entryCounter) Grammar(instructions, width, alternatives, work uint64) error {
	i, err := c.check()
	if err != nil {
		return err
	}
	if c.grammar {
		return i.scopeFailure(ErrIndexInvariant)
	}
	c.grid, err = grammarPhysicalCounts(instructions, width, alternatives, work)
	if err != nil {
		return i.scopeFailure(err)
	}
	c.grammar = true
	return nil
}
func (h *Inspection) CountEntry(parent StateCharge, span Span, visit func(EntryCounter) error) (_ EntryCharge, err error) {
	i, err := h.check()
	if err != nil {
		return EntryCharge{}, err
	}
	if !i.outer.stateActive || i.outer.object.active || i.outer.object.pending.id != 0 || parent.owner != i || parent.epoch != h.epoch || parent.id != i.outer.stateID || span.End <= span.Start || visit == nil || i.outer.entryActive || i.outer.pendingEntry.id != 0 || i.outer.countActive {
		return EntryCharge{}, i.scopeFailure(ErrIndexInvariant)
	}
	if err := h.CheckSpan(i.outer.stateSource, span); err != nil {
		return EntryCharge{}, err
	}
	if span.Start < i.outer.stateSpan.Start || span.End > i.outer.stateSpan.End {
		return EntryCharge{}, i.scopeFailure(ErrIndexInvariant)
	}
	if i.outer.sequence == ^uint64(0) {
		return EntryCharge{}, i.scopeFailure(ErrIndexInvariant)
	}
	i.outer.sequence++
	id := i.outer.sequence
	i.outer.countActive = true
	counter := entryCounter{h: h, id: id, active: true}
	defer func() {
		counter.active = false
		i.outer.countActive = false
		if err != nil {
			i.scopeFailure(err)
		}
	}()
	if err = visit(&counter); err != nil {
		return EntryCharge{}, err
	}
	var counts entryRecordCounts
	if counter.grammar {
		counts.runs, err = chargeAdd(counter.runs, 1)
		if err != nil {
			return EntryCharge{}, err
		}
		counts.runRows, err = chargeAdd(counts.runs, 1)
		if err != nil {
			return EntryCharge{}, err
		}
		counts.gridBits, counts.frontierIDs = counter.grid.bits, counter.grid.frontier
	}
	if _, err = entryPhysicalCharge(counts); err != nil {
		return EntryCharge{}, err
	}
	i.outer.pendingEntry = entryChargeData{id: id, parent: parent.id, source: i.outer.stateSource, span: span, counts: counts, grammar: counter.grammar}
	return EntryCharge{owner: i, epoch: h.epoch, id: id, parent: parent.id}, nil
}
