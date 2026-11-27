package tomlinspect

import "unsafe"

const (
	objectTree                       = treeArrays
	relationObjectNames RelationKind = 255
)

// ObjectCharge belongs to one completed source-field count. Its distinct
// wrapped representation cannot be converted to a StateCharge by a borrower.
type ObjectCharge struct{ state StateCharge }

// ObjectCounter counts actual source fields, never a declared count or quota.
// The immutable value cannot adopt a later count or inspection epoch.
type ObjectCounter struct {
	h  *Inspection
	id uint64
}

// ObjectNameComparer supplies exact source-span equality, not authority.
// The real projection view implements it once for the complete inspection.
type ObjectNameComparer interface {
	// CompareObjectNames compares exact decoded names from the same source.
	CompareObjectNames(Span, Span) (int, error)
}

type objectChargeData struct {
	id     uint64
	span   Span
	fields uint64
}

type objectInspectionState struct {
	pending  objectChargeData
	id       uint64
	span     Span
	fields   uint64
	active   bool
	comparer ObjectNameComparer
}

// ObjectPreflightHeaderBytes is the exact extra fixed native header storage.
// It remains charged to the original actor budget; the slab/cache do not grow.
// Sizeof is a compile-time layout query, with no unsafe memory access.
const ObjectPreflightHeaderBytes = unsafe.Sizeof(objectInspectionState{}) + unsafe.Sizeof(regionBook{}) + unsafe.Sizeof(regionOwner{})

// Field counts exactly one actually visited object field in this count.
func (c ObjectCounter) Field() error {
	i, err := c.h.check()
	if err != nil {
		return err
	}
	if !i.outer.countActive || i.outer.sequence != c.id || i.outer.object.pending.id != 0 {
		return i.scopeFailure(ErrIndexInvariant)
	}
	i.outer.object.pending.fields, err = chargeAdd(i.outer.object.pending.fields, 1)
	if err != nil {
		return i.scopeFailure(err)
	}
	return nil
}

func (i *Inspector) objectAdmissible() error {
	if !i.outer.tomlDone || i.outer.object.active || i.outer.object.pending.id != 0 || i.outer.entryActive || i.outer.pendingEntry.id != 0 || i.outer.pendingState.id != 0 || i.outer.countActive || i.outer.comparing {
		return i.scopeFailure(ErrIndexInvariant)
	}
	// The current caller may retain the receipt's ONE identity relation.
	// An object book never overlaps semantic sentinel/host trees or an NFA.
	if i.outer.stateActive && (i.outer.books[1].trees[0] || i.outer.books[1].trees[2]) {
		return i.scopeFailure(ErrIndexInvariant)
	}
	return nil
}

// CountObject seals actual fields in the same guarded sandbox source.
func (h *Inspection) CountObject(source int, span Span, visit func(ObjectCounter) error) (_ ObjectCharge, err error) {
	i, err := h.check()
	if err != nil {
		return ObjectCharge{}, err
	}
	if err := i.objectAdmissible(); err != nil {
		return ObjectCharge{}, err
	}
	if source != 1 || span.End <= span.Start || visit == nil {
		return ObjectCharge{}, i.scopeFailure(ErrIndexInvariant)
	}
	if err := h.CheckSpan(source, span); err != nil {
		return ObjectCharge{}, err
	}
	if i.outer.stateActive && (span.Start < i.outer.stateSpan.Start || span.End > i.outer.stateSpan.End) {
		return ObjectCharge{}, i.scopeFailure(ErrIndexInvariant)
	}
	if err := i.outer.sources[1].Check(i.outer.ctx); err != nil {
		return ObjectCharge{}, i.scopeFailure(err)
	}
	if i.outer.sequence == ^uint64(0) {
		return ObjectCharge{}, i.scopeFailure(ErrIndexInvariant)
	}
	i.outer.sequence++
	id := i.outer.sequence
	i.outer.countActive = true
	counter := ObjectCounter{h: h, id: id}
	defer func() {
		i.outer.countActive = false
		if err != nil {
			i.scopeFailure(err)
		}
	}()
	if err = visit(counter); err != nil {
		return ObjectCharge{}, err
	}
	if err = i.outer.sources[1].Check(i.outer.ctx); err != nil {
		return ObjectCharge{}, err
	}
	fields, err := chargeAdd(i.outer.object.pending.fields, 1)
	if err != nil {
		return ObjectCharge{}, err
	}
	if _, err = objectPhysicalCharge(fields); err != nil {
		return ObjectCharge{}, err
	}
	i.outer.object.pending = objectChargeData{id: id, span: span, fields: fields}
	return ObjectCharge{state: StateCharge{owner: i, epoch: h.epoch, id: id}}, nil
}

// ObjectRegion is one exact object-name index. Its complete metadata retires
// before its caller invokes that object's visitor or a child object prepass.
type ObjectRegion struct {
	h      *Inspection
	id     uint64
	closed bool
}

// BeginObject admits the completed count and captures ONE exact comparator.
func (h *Inspection) BeginObject(token ObjectCharge, compare ObjectNameComparer) (ObjectRegion, error) {
	i, err := h.check()
	if err != nil {
		return ObjectRegion{}, err
	}
	p := i.outer.object.pending
	if token.state.owner != i || token.state.epoch != h.epoch || token.state.id == 0 || token.state.id != p.id || nilInterface(compare) || i.outer.object.active || i.outer.entryActive || i.outer.countActive || i.outer.comparing {
		return ObjectRegion{}, i.scopeFailure(ErrIndexInvariant)
	}
	if i.outer.stateActive && (i.outer.books[1].trees[0] || i.outer.books[1].trees[2]) {
		return ObjectRegion{}, i.scopeFailure(ErrIndexInvariant)
	}
	if err := i.outer.sources[1].Check(i.outer.ctx); err != nil {
		return ObjectRegion{}, i.scopeFailure(err)
	}
	charge, err := objectPhysicalCharge(p.fields)
	if err != nil {
		return ObjectRegion{}, i.scopeFailure(err)
	}
	i.outer.object = objectInspectionState{id: p.id, span: p.span, fields: p.fields, active: true, comparer: compare}
	global := uint64(0)
	if i.outer.stateActive {
		global, err = statePhysicalCharge(i.outer.stateCounts)
		if err != nil {
			return ObjectRegion{}, i.scopeFailure(err)
		}
	}
	if err := i.installCharge(global, 0, 0); err != nil {
		return ObjectRegion{}, err
	}
	ref, err := i.outer.regions.begin(objectRegionSlot)
	if err != nil {
		return ObjectRegion{}, i.scopeFailure(err)
	}
	if err := i.outer.regions.setCharge(ref, charge); err != nil {
		return ObjectRegion{}, i.scopeFailure(err)
	}
	i.outer.books[objectRegionSlot] = regionBook{ref: ref}
	g := ObjectRegion{h: h, id: p.id}
	book := &i.outer.books[objectRegionSlot]
	book.trees[objectTree] = true
	book.comparisons[objectTree] = regionComparison{h: h, stateID: p.id, kind: relationObjectNames}
	var descriptor [192]byte
	descriptor[0] = 255
	putSpan(descriptor[:], 8, p.span)
	write64(descriptor[:], 56, p.fields-1)
	if _, err := i.appendMetadata(objectRegionSlot, layoutBinding, descriptor[:]); err != nil {
		return ObjectRegion{}, err
	}
	return g, nil
}

func (g *ObjectRegion) check() (*Inspector, error) {
	if g == nil || g.h == nil {
		return nil, ErrIndexInvariant
	}
	if g.closed {
		return nil, g.h.invalid()
	}
	i, err := g.h.check()
	if err != nil {
		return nil, err
	}
	if !i.outer.object.active || i.outer.object.id != g.id {
		return nil, i.scopeFailure(ErrIndexInvariant)
	}
	if err := i.outer.regions.check(i.outer.books[objectRegionSlot].ref); err != nil {
		return nil, i.scopeFailure(err)
	}
	return i, nil
}
