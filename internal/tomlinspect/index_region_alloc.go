package tomlinspect

// allocate reuses only fully retired pages, or delegates growth to the SAME
// checked physical allocator. It changes no admitted page/input bound.
func (r *regionPages) allocate(ref regionRef, kind, class byte) (uint32, []byte, int, error) {
	if err := r.check(ref); err != nil {
		return 0, nil, 0, err
	}
	if kind < pageDirectoryRoot || kind > pageBTreeBranch {
		return 0, nil, 0, r.fail(ErrIndexInvariant)
	}
	owner := &r.owners[ref.slot]
	if !owner.charged || uint64(owner.pages) >= owner.quota {
		return 0, nil, 0, r.fail(ErrIndexInvariant)
	}
	if owner.pages == ^uint32(0) {
		return 0, nil, 0, r.fail(ErrIndexInvariant)
	}
	var id uint32
	var data []byte
	var slot int
	var err error
	if r.freeHead == 0 {
		if r.freeCount != 0 {
			return 0, nil, 0, r.fail(ErrIndexInvariant)
		}
		id, data, slot, err = r.pages.allocate(kind, class)
	} else {
		if r.freeCount == 0 {
			return 0, nil, 0, r.fail(ErrIndexInvariant)
		}
		id = r.freeHead
		data, slot, err = r.pages.pin(id)
		if err == nil {
			next := read32(data, 28)
			if r.pages.tags[slot].held || r.pages.tags[slot].pins != 1 || next >= r.pages.nextPage || next == id {
				r.pages.release(slot, false)
				return 0, nil, 0, r.fail(ErrIndexCorrupt)
			}
			r.freeHead, r.freeCount = next, r.freeCount-1
			if r.freeCount == 0 && r.freeHead != 0 || r.freeCount != 0 && r.freeHead == 0 {
				r.pages.release(slot, false)
				return 0, nil, 0, r.fail(ErrIndexCorrupt)
			}
			clear(data)
			write32(data, 0, indexMagic)
			write32(data, 4, id)
			data[8], data[9] = kind, class
			r.pages.tags[slot].dirty = true
		}
	}
	if err != nil {
		return 0, nil, 0, r.fail(err)
	}
	write16(data, 10, ref.slot)
	// Every new member is charged before linking can fail. Failure poisons
	// the complete inspection; the actual lease then closes all resources.
	owner.pages++
	if owner.last == 0 {
		if owner.first != 0 || owner.pages != 1 {
			r.pages.release(slot, true)
			return 0, nil, 0, r.fail(ErrIndexInvariant)
		}
		owner.first = id
	} else {
		tail, tailSlot, failure := r.pages.pin(owner.last)
		if failure != nil {
			r.pages.release(slot, true)
			return 0, nil, 0, r.fail(failure)
		}
		if read16(tail, 10) != ref.slot || read32(tail, 28) != 0 {
			r.pages.release(tailSlot, false)
			r.pages.release(slot, true)
			return 0, nil, 0, r.fail(ErrIndexCorrupt)
		}
		write32(tail, 28, id)
		r.pages.release(tailSlot, true)
	}
	owner.last = id
	return id, data, slot, nil
}

// retire invalidates this phase before publication to the allocator free
// chain. It touches only its tail and the fixed64 cache tags, never every
// live TOML/global page or all prior phases. Cached owner links are flushed
// before invalidation so no unflushed chain is lost on reuse.
func (r *regionPages) retire(ref regionRef) error {
	if err := r.check(ref); err != nil {
		return err
	}
	owner := &r.owners[ref.slot]
	if owner.uses != 0 {
		return r.fail(ErrIndexInvariant)
	}
	for slot := range r.pages.tags {
		tag := &r.pages.tags[slot]
		if tag.id != 0 && read16(r.pages.view(slot), 10) == ref.slot && (tag.pins != 0 || tag.held) {
			return r.fail(ErrIndexInvariant)
		}
	}
	if owner.pages > ^uint32(0)-r.freeCount {
		return r.fail(ErrIndexInvariant)
	}
	owner.active = false
	if owner.pages == 0 {
		if owner.first != 0 || owner.last != 0 {
			return r.fail(ErrIndexInvariant)
		}
		return nil
	}
	if owner.first == 0 || owner.last == 0 {
		return r.fail(ErrIndexInvariant)
	}
	tail, tailSlot, err := r.pages.pin(owner.last)
	if err != nil {
		return r.fail(err)
	}
	if read16(tail, 10) != ref.slot || read32(tail, 28) != 0 {
		r.pages.release(tailSlot, false)
		return r.fail(ErrIndexCorrupt)
	}
	write32(tail, 28, r.freeHead)
	r.pages.release(tailSlot, true)
	for slot := range r.pages.tags {
		tag := &r.pages.tags[slot]
		if tag.id == 0 || read16(r.pages.view(slot), 10) != ref.slot {
			continue
		}
		if err := r.pages.flush(slot); err != nil {
			return r.fail(err)
		}
		*tag = pageTag{}
	}
	r.freeHead = owner.first
	r.freeCount += owner.pages
	owner.first, owner.last, owner.pages = 0, 0, 0
	return r.stopped()
}
