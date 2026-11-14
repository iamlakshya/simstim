package tomlinspect

// recordDirectory borrows the SAME two-level physical page directory for
// one fixed metadata class. It carries no source, value decoder or lifetime.
// Only the TOML names class supplies its existing held-directory pin set.
type recordDirectory struct {
	region *regionPages
	ref    regionRef
	*indexPages
	root      *uint32
	class     byte
	held      *[10]uint32
	heldCount *int
}

func (d *recordDirectory) retain(slot int) error {
	tag := &d.tags[slot]
	if tag.held {
		return nil
	}
	if *d.heldCount == len(d.held) {
		return ErrIndexInvariant
	}
	tag.held = true
	tag.pins++
	d.pins++
	d.held[*d.heldCount] = tag.id
	(*d.heldCount)++
	d.counters.PeakPins = max(d.counters.PeakPins, d.pins)
	return nil
}

func (d *recordDirectory) page(virtual uint32, create bool) (uint32, error) {
	page, found, err := d.locate(virtual, create)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, ErrIndexCorrupt
	}
	return page, nil
}

func (d *recordDirectory) locate(virtual uint32, create bool) (uint32, bool, error) {
	class := d.class
	if virtual >= directorySlots*directorySlots {
		return 0, false, ErrIndexInvariant
	}
	var root []byte
	var slot int
	var err error
	if *d.root == 0 {
		if !create {
			return 0, false, nil
		}
		*d.root, root, slot, err = d.allocate(pageDirectoryRoot, class)
	} else {
		root, slot, err = d.pin(*d.root)
	}
	if err != nil {
		return 0, false, err
	}
	if root[8] != pageDirectoryRoot || root[9] != class {
		d.release(slot, false)
		return 0, false, ErrIndexCorrupt
	}
	if d.held != nil {
		if err := d.retain(slot); err != nil {
			d.release(slot, false)
			return 0, false, err
		}
	}
	offset := indexPageHeader + int(virtual/directorySlots)*4
	child := read32(root, offset)
	dirty := false
	if child == 0 && create {
		var next []byte
		var nextSlot int
		child, next, nextSlot, err = d.allocate(pageDirectoryLeaf, class)
		_ = next
		if err == nil {
			if d.held != nil {
				err = d.retain(nextSlot)
			}
			d.release(nextSlot, true)
		}
		if err != nil {
			d.release(slot, false)
			return 0, false, err
		}
		write32(root, offset, child)
		dirty = true
	}
	d.release(slot, dirty)
	if child == 0 {
		return 0, false, nil
	}
	leaf, leafSlot, err := d.pin(child)
	if err != nil {
		return 0, false, err
	}
	if leaf[8] != pageDirectoryLeaf || leaf[9] != class {
		d.release(leafSlot, false)
		return 0, false, ErrIndexCorrupt
	}
	if d.held != nil {
		if err := d.retain(leafSlot); err != nil {
			d.release(leafSlot, false)
			return 0, false, err
		}
	}
	offset = indexPageHeader + int(virtual%directorySlots)*4
	page := read32(leaf, offset)
	dirty = false
	if page == 0 && create {
		var data []byte
		var dataSlot int
		page, data, dataSlot, err = d.allocate(pageRecordData, class)
		_ = data
		if err != nil {
			d.release(leafSlot, false)
			return 0, false, err
		}
		d.release(dataSlot, true)
		write32(leaf, offset, page)
		dirty = true
	}
	d.release(leafSlot, dirty)
	if page == 0 {
		return 0, false, nil
	}
	return page, true, nil
}
