package tomlinspect

func (s *numericTrees) walk(tree byte, first uint32, visit func(numericKey, numericValue) error) error {
	if visit == nil || s.comparison != nil && !s.prefixGrouped {
		return ErrIndexInvariant
	}
	page, err := s.descend(tree, numericKey{first: first}, nil)
	if err != nil || page == 0 {
		return err
	}
	at := 0
	var previous numericKey
	havePrevious := false
	var pagesSeen uint32
	for page != 0 {
		data, slot, err := s.pin(page)
		if err != nil {
			return err
		}
		if data[8] != pageBTreeLeaf || data[9] != tree {
			s.release(slot, false)
			return ErrIndexCorrupt
		}
		n := int(read16(data, 16))
		if n > leafCapacity {
			s.release(slot, false)
			return ErrIndexCorrupt
		}
		if at == 0 {
			pagesSeen++
			if pagesSeen >= s.nextPage {
				s.release(slot, false)
				return ErrIndexCorrupt
			}
			at, _, err = s.leafPosition(data, numericKey{first: first})
			if err != nil {
				s.release(slot, false)
				return err
			}
		}
		if at >= n {
			next := read32(data, 20)
			s.release(slot, false)
			if next >= s.nextPage {
				return ErrIndexCorrupt
			}
			page, at = next, 0
			continue
		}
		offset := indexPageHeader + at*leafRecordBytes
		key := readKey(data, offset)
		var value numericValue
		copy(value[:], data[offset+8:offset+leafRecordBytes])
		s.release(slot, false)
		if havePrevious {
			order, err := s.order(previous, key)
			if err != nil {
				return err
			}
			if order >= 0 {
				return ErrIndexCorrupt
			}
		}
		previous, havePrevious = key, true
		if key.first > first {
			return nil
		}
		if key.first == first {
			if err := visit(key, value); err != nil {
				return err
			}
		}
		at++
	}
	return nil
}
