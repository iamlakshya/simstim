package tomlinspect

func objectPhysicalCharge(fields uint64) (uint64, error) {
	descriptor, err := packedRecordPages(192, 1)
	if err != nil {
		return 0, err
	}
	names, err := packedRecordPages(96, fields)
	if err != nil {
		return 0, err
	}
	tree, err := numericTreePages(fields)
	if err != nil {
		return 0, err
	}
	return chargeSum(descriptor, names, tree, uint64(partialTreePages))
}

func objectName(h *Inspection, objectID, id uint64) (Span, error) {
	g := ObjectRegion{h: h, id: objectID}
	i, err := g.check()
	if err != nil {
		return Span{}, err
	}
	var raw [96]byte
	if err := i.readMetadata(objectRegionSlot, layoutHost, id, raw[:]); err != nil {
		return Span{}, err
	}
	name := getSpan(raw[:], 8)
	if read64(raw[:], 0) != 1 || getSpan(raw[:], 24) != name || getSpan(raw[:], 40) != (Span{}) || read64(raw[:], 56) != 0 {
		return Span{}, i.scopeFailure(ErrIndexCorrupt)
	}
	if err := g.checkName(i, name); err != nil {
		return Span{}, err
	}
	return name, nil
}

func (g *ObjectRegion) checkName(i *Inspector, name Span) error {
	if name.End <= name.Start || name.Start < i.outer.object.span.Start || name.End > i.outer.object.span.End {
		return i.scopeFailure(ErrIndexCorrupt)
	}
	return g.h.CheckSpan(1, name)
}

// PutName indexes one actually visited field. A duplicate is a data result,
// not a Core error; no failed IO, source or comparison becomes that result.
func (g *ObjectRegion) PutName(name Span) (bool, error) {
	i, err := g.check()
	if err != nil {
		return false, err
	}
	if err := i.metadataWriteAllowed(); err != nil {
		return false, err
	}
	if err := g.checkName(i, name); err != nil {
		return false, err
	}
	book := &i.outer.books[objectRegionSlot]
	if err := i.outer.regions.enter(book.ref); err != nil {
		return false, i.scopeFailure(err)
	}
	defer i.outer.regions.leave(book.ref)
	var raw [96]byte
	write64(raw[:], 0, 1)
	putSpan(raw[:], 8, name)
	putSpan(raw[:], 24, name)
	id, err := i.appendMetadata(objectRegionSlot, layoutHost, raw[:])
	if err != nil {
		return false, err
	}
	view, err := i.outer.regions.treeView(book.ref, &book.roots)
	if err != nil {
		return false, i.scopeFailure(err)
	}
	view.comparison = &book.comparisons[objectTree]
	view.prefixGrouped = true
	if id > uint64(^uint32(0)) {
		return false, i.scopeFailure(ErrIndexCorrupt)
	}
	key := numericKey{first: 1, second: uint32(id)}
	_, found, err := view.get(objectTree, key)
	if err != nil {
		return false, i.scopeFailure(err)
	}
	if found {
		return false, nil
	}
	var value numericValue
	write64(value[:], 0, id)
	if err := view.put(objectTree, key, value); err != nil {
		return false, i.scopeFailure(err)
	}
	book.rows[objectTree]++
	return true, nil
}

// FinishObject requires the complete counted pass before retiring metadata.
func (g *ObjectRegion) FinishObject() error {
	i, err := g.check()
	if err != nil {
		return err
	}
	if i.outer.comparing || i.outer.object.fields == 0 || uint64(i.outer.books[objectRegionSlot].classes[layoutHost].count) != i.outer.object.fields-1 {
		return i.scopeFailure(ErrIndexInvariant)
	}
	return g.Close()
}

// Close retires only this object's resources. It grants no completed pass;
// callers must use FinishObject before invoking an object visitor.
func (g *ObjectRegion) Close() error {
	if g == nil || g.closed {
		return nil
	}
	i, err := g.check()
	if err != nil {
		return err
	}
	if i.outer.comparing || i.outer.countActive {
		return i.scopeFailure(ErrIndexInvariant)
	}
	if err := i.outer.regions.retire(i.outer.books[objectRegionSlot].ref); err != nil {
		return i.scopeFailure(err)
	}
	g.closed = true
	i.outer.books[objectRegionSlot] = regionBook{}
	i.outer.object = objectInspectionState{}
	if err := i.outer.sources[1].Check(i.outer.ctx); err != nil {
		return i.scopeFailure(err)
	}
	return nil
}
