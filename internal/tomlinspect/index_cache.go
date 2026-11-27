package tomlinspect

func (s *indexPages) view(slot int) []byte {
	return s.data[slot*indexPageBytes : (slot+1)*indexPageBytes]
}

func (s *indexPages) flush(slot int) error {
	tag := &s.tags[slot]
	if tag.id == 0 || !tag.dirty {
		return nil
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	data := s.view(slot)
	write32(data, indexCRCOffset, pageCRC(data))
	n, err := s.file.WriteAt(data, int64(tag.id-1)*indexPageBytes)
	if n > 0 {
		s.counters.WrittenBytes += uint64(n)
	}
	if stopped := s.ctx.Err(); stopped != nil {
		return stopped
	}
	if err != nil || n != len(data) {
		return ErrIndexIO
	}
	tag.dirty = false
	s.counters.PageWrites++
	return nil
}

func (s *indexPages) victim() (int, error) {
	index := -1
	for slot := range s.tags {
		tag := &s.tags[slot]
		if tag.pins != 0 {
			continue
		}
		if tag.id == 0 {
			return slot, nil
		}
		if index < 0 || tag.used < s.tags[index].used {
			index = slot
		}
	}
	if index < 0 {
		return 0, ErrIndexInvariant
	}
	if err := s.flush(index); err != nil {
		return 0, err
	}
	return index, nil
}

func (s *indexPages) pin(id uint32) ([]byte, int, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, 0, err
	}
	if id == 0 || id >= s.nextPage {
		return nil, 0, ErrIndexCorrupt
	}
	for slot := range s.tags {
		if s.tags[slot].id == id {
			s.clock++
			s.tags[slot].used = s.clock
			s.tags[slot].pins++
			s.pins++
			s.counters.PeakPins = max(s.counters.PeakPins, s.pins)
			return s.view(slot), slot, nil
		}
	}
	slot, err := s.victim()
	if err != nil {
		return nil, 0, err
	}
	data := s.view(slot)
	s.tags[slot] = pageTag{}
	n, err := s.file.ReadAt(data, int64(id-1)*indexPageBytes)
	if n > 0 {
		s.counters.ReadBytes += uint64(n)
	}
	if stopped := s.ctx.Err(); stopped != nil {
		return nil, 0, stopped
	}
	if err != nil || n != len(data) {
		return nil, 0, ErrIndexIO
	}
	if read32(data, 0) != indexMagic || read32(data, 4) != id || read32(data, indexCRCOffset) != pageCRC(data) {
		return nil, 0, ErrIndexCorrupt
	}
	if data[8] < pageDirectoryRoot || data[8] > pageBTreeBranch {
		return nil, 0, ErrIndexCorrupt
	}
	s.clock++
	s.tags[slot] = pageTag{id: id, used: s.clock, pins: 1}
	s.pins++
	s.counters.PageReads++
	s.counters.PeakPins = max(s.counters.PeakPins, s.pins)
	return data, slot, nil
}

func (s *indexPages) release(slot int, dirty bool) {
	tag := &s.tags[slot]
	if tag.pins == 0 {
		panic("owned index page pin invariant")
	}
	tag.pins--
	tag.dirty = tag.dirty || dirty
	s.pins--
}

func (s *indexPages) allocate(kind, class byte) (uint32, []byte, int, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, nil, 0, err
	}
	// This is the source-charge proof envelope, not a user count cap.
	// Exceeding it is an implementation defect; no result may be granted.
	if s.nextPage > s.maxPages || kind < pageDirectoryRoot || kind > pageBTreeBranch {
		return 0, nil, 0, ErrIndexInvariant
	}
	slot, err := s.victim()
	if err != nil {
		return 0, nil, 0, err
	}
	id := s.nextPage
	s.nextPage++
	data := s.view(slot)
	clear(data)
	write32(data, 0, indexMagic)
	write32(data, 4, id)
	data[8], data[9] = kind, class
	s.clock++
	s.tags[slot] = pageTag{id: id, used: s.clock, pins: 1, dirty: true}
	s.pins++
	s.counters.Pages++
	s.counters.PeakPins = max(s.counters.PeakPins, s.pins)
	return id, data, slot, nil
}
