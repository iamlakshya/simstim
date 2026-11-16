package tomlinspect

import "context"

// Inspection is an immutable handle to one complete admitted source scope.
// The small handle is unique per callback; mutable fixed state stays in the
// reusable Inspector. A retained pointer cannot adopt a later scope's epoch.
type Inspection struct {
	owner *Inspector
	epoch uint64
}

type StateCharge struct {
	owner     *Inspector
	epoch, id uint64
}
type EntryCharge struct {
	owner             *Inspector
	epoch, id, parent uint64
}

type stateChargeData struct {
	id     uint64
	source int
	span   Span
	counts stateRecordCounts
}
type entryChargeData struct {
	id, parent uint64
	source     int
	span       Span
	counts     entryRecordCounts
	grammar    bool
}

type regionBook struct {
	rows        [treeCount]uint64
	comparisons [treeCount]regionComparison
	ref         regionRef
	classes     [metadataLayoutCount]recordClassState
	roots       [treeCount]uint32
	trees       [treeCount]bool
}

type inspectionState struct {
	ctx                                      context.Context
	sources                                  [2]MetadataSource
	workspace                                []byte
	file                                     IndexFile
	epoch                                    uint64
	active, tomlDone, tomlRunning, comparing bool
	failure                                  error
	regions                                  regionPages
	books                                    [metadataRegionSlots]regionBook
	tomlBound, maxBound                      uint64
	sequence                                 uint64
	pendingState                             stateChargeData
	pendingEntry                             entryChargeData
	stateID                                  uint64
	stateSource                              int
	stateSpan                                Span
	stateCounts                              stateRecordCounts
	stateActive                              bool
	entryID                                  uint64
	entryCounts                              entryRecordCounts
	entryActive, runsBegun, grammarBegun     bool
	gridOpened, frontierOpened               bool
	frontierLength                           uint64
	countActive                              bool
	object                                   objectInspectionState
}

func (i *Inspector) scopeFailure(err error) error {
	if err == nil {
		return nil
	}
	if i == nil {
		return ErrIndexInvariant
	}
	if i.outer.failure == nil {
		i.outer.failure = err
	}
	return i.outer.failure
}
func (h *Inspection) check() (*Inspector, error) {
	if h == nil || h.owner == nil {
		return nil, ErrIndexInvariant
	}
	i := h.owner
	if !i.outer.active || !i.active || i.epoch != h.epoch || i.outer.epoch != h.epoch || i.outer.ctx == nil {
		return nil, ErrIndexInvariant
	}
	if i.outer.failure != nil {
		return nil, i.outer.failure
	}
	if err := i.outer.ctx.Err(); err != nil {
		return nil, i.scopeFailure(err)
	}
	if err := i.outer.regions.failure; err != nil {
		return nil, i.scopeFailure(err)
	}
	return i, nil
}
func (h *Inspection) Context() (context.Context, error) {
	i, err := h.check()
	if err != nil {
		return nil, err
	}
	return i.outer.ctx, nil
}
func (h *Inspection) Source(id int) (MetadataSource, error) {
	i, err := h.check()
	if err != nil {
		return nil, err
	}
	if id < 0 || id >= len(i.outer.sources) {
		return nil, i.scopeFailure(ErrIndexInvariant)
	}
	return i.outer.sources[id], nil
}
func (h *Inspection) CheckSpan(id int, span Span) error {
	i, err := h.check()
	if err != nil {
		return err
	}
	if id < 0 || id >= len(i.outer.sources) || span.Start < 0 || span.End < span.Start || span.End > i.outer.sources[id].Size() {
		return i.scopeFailure(ErrIndexInvariant)
	}
	return nil
}
func (i *Inspector) detachScope() {
	epoch := i.outer.regions.epoch
	i.outer = inspectionState{regions: regionPages{epoch: epoch}}
}

func (h *Inspection) invalid() error {
	if h != nil && h.owner != nil && h.owner.outer.active && h.owner.epoch == h.epoch {
		return h.owner.scopeFailure(ErrIndexInvariant)
	}
	return ErrIndexInvariant
}
