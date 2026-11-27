package tomlinspect

// BitGrid and Frontier borrow ONLY the current entry's grammar phase.
type BitGrid struct {
	entry *EntryRegion
	bits  uint64
}
type Frontier struct{ entry *EntryRegion }

func (e *EntryRegion) RunOffsets() (Tree, error) {
	i, err := e.check()
	if err != nil {
		return Tree{}, err
	}
	if err = i.metadataWriteAllowed(); err != nil {
		return Tree{}, err
	}
	if i.outer.runsBegun || i.outer.entryCounts.runRows == 0 {
		return Tree{}, i.scopeFailure(ErrIndexInvariant)
	}
	i.outer.runsBegun = true
	i.outer.books[2].trees[0] = true
	return Tree{state: e.state, entry: e, slot: 2, which: 0}, nil
}
func (e *EntryRegion) NewBitGrid(bits uint64) (BitGrid, error) {
	i, err := e.check()
	if err != nil {
		return BitGrid{}, err
	}
	if err = i.metadataWriteAllowed(); err != nil {
		return BitGrid{}, err
	}
	if i.outer.gridOpened || bits == 0 || bits != i.outer.entryCounts.gridBits {
		return BitGrid{}, i.scopeFailure(ErrIndexInvariant)
	}
	i.outer.gridOpened = true
	return BitGrid{entry: e, bits: bits}, nil
}
func (g BitGrid) access(bit uint64, set bool) (bool, error) {
	if g.entry == nil {
		return false, ErrIndexInvariant
	}
	i, err := g.entry.check()
	if err != nil {
		return false, err
	}
	if i.outer.comparing || !i.outer.gridOpened || g.bits != i.outer.entryCounts.gridBits || bit >= g.bits {
		return false, i.scopeFailure(ErrIndexInvariant)
	}
	if set {
		if err = i.metadataWriteAllowed(); err != nil {
			return false, err
		}
	}
	book := &i.outer.books[3]
	if err = i.outer.regions.enter(book.ref); err != nil {
		return false, i.scopeFailure(err)
	}
	defer i.outer.regions.leave(book.ref)
	byteIndex := bit / 8
	virtual := byteIndex / uint64(indexPageBytes-indexPageHeader)
	if virtual >= directorySlots*directorySlots {
		return false, i.scopeFailure(ErrIndexInvariant)
	}
	dir, err := i.outer.regions.directoryView(book.ref, layoutBits, &book.classes[layoutBits].root)
	if err != nil {
		return false, i.scopeFailure(err)
	}
	page, found, err := dir.locate(uint32(virtual), set)
	if err != nil {
		return false, i.scopeFailure(err)
	}
	if !found {
		return false, nil
	}
	data, pin, err := i.outer.regions.pin(book.ref, page)
	if err != nil {
		return false, i.scopeFailure(err)
	}
	if data[8] != pageRecordData || data[9] != layoutBits {
		i.store.release(pin, false)
		return false, i.scopeFailure(ErrIndexCorrupt)
	}
	offset := indexPageHeader + int(byteIndex%uint64(indexPageBytes-indexPageHeader))
	mask := byte(1) << uint(bit%8)
	present := data[offset]&mask != 0
	if set {
		data[offset] |= mask
	}
	i.store.release(pin, set)
	return present, nil
}
func (g BitGrid) Test(bit uint64) (bool, error) { return g.access(bit, false) }
func (g BitGrid) Set(bit uint64) error          { _, err := g.access(bit, true); return err }
func (e *EntryRegion) Frontier() (Frontier, error) {
	i, err := e.check()
	if err != nil {
		return Frontier{}, err
	}
	if err = i.metadataWriteAllowed(); err != nil {
		return Frontier{}, err
	}
	if i.outer.frontierOpened || i.outer.entryCounts.frontierIDs == 0 {
		return Frontier{}, i.scopeFailure(ErrIndexInvariant)
	}
	i.outer.frontierOpened = true
	i.outer.frontierLength = 0
	return Frontier{entry: e}, nil
}
func (f Frontier) Push(id uint64) error {
	if f.entry == nil {
		return ErrIndexInvariant
	}
	i, err := f.entry.check()
	if err != nil {
		return err
	}
	if err = i.metadataWriteAllowed(); err != nil {
		return err
	}
	if !i.outer.frontierOpened || id >= i.outer.entryCounts.gridBits || i.outer.frontierLength >= i.outer.entryCounts.frontierIDs {
		return i.scopeFailure(ErrIndexInvariant)
	}
	position := i.outer.frontierLength
	block := position/8 + 1
	var raw [64]byte
	if block <= uint64(i.outer.books[3].classes[layoutFrontier].count) {
		if err = i.readMetadata(3, layoutFrontier, block, raw[:]); err != nil {
			return err
		}
	}
	write64(raw[:], int(position%8)*8, id)
	if block > uint64(i.outer.books[3].classes[layoutFrontier].count) {
		if _, err = i.appendMetadata(3, layoutFrontier, raw[:]); err != nil {
			return err
		}
	} else if err = i.writeMetadata(3, layoutFrontier, block, raw[:]); err != nil {
		return err
	}
	i.outer.frontierLength++
	return nil
}
func (f Frontier) Pop() (uint64, bool, error) {
	if f.entry == nil {
		return 0, false, ErrIndexInvariant
	}
	i, err := f.entry.check()
	if err != nil {
		return 0, false, err
	}
	if i.outer.comparing || !i.outer.frontierOpened {
		return 0, false, i.scopeFailure(ErrIndexInvariant)
	}
	if i.outer.frontierLength == 0 {
		return 0, false, nil
	}
	position := i.outer.frontierLength - 1
	var raw [64]byte
	if err = i.readMetadata(3, layoutFrontier, position/8+1, raw[:]); err != nil {
		return 0, false, err
	}
	id := read64(raw[:], int(position%8)*8)
	if id >= i.outer.entryCounts.gridBits {
		return 0, false, i.scopeFailure(ErrIndexCorrupt)
	}
	i.outer.frontierLength = position
	return id, true, nil
}
