package tomlinspect

// floor is a numeric predecessor lookup on the SAME B+tree. It keeps only
// the existing fixed ancestor path; there is no record scan or extra index.
func (s *numericTrees) floor(tree byte, key numericKey) (numericKey, numericValue, bool, error) {
	var value numericValue
	if s.comparison != nil {
		return numericKey{}, value, false, ErrIndexInvariant
	}
	s.counters.NumericLookups++
	var path treePath
	page, err := s.descend(tree, key, &path)
	if err != nil || page == 0 {
		return numericKey{}, value, false, err
	}
	data, slot, err := s.pin(page)
	if err != nil {
		return numericKey{}, value, false, err
	}
	if data[8] != pageBTreeLeaf || data[9] != tree {
		s.release(slot, false)
		return numericKey{}, value, false, ErrIndexCorrupt
	}
	n := int(read16(data, 16))
	if n == 0 || n > leafCapacity {
		s.release(slot, false)
		return numericKey{}, value, false, ErrIndexCorrupt
	}
	at, exact, err := s.leafPosition(data, key)
	if err != nil {
		s.release(slot, false)
		return numericKey{}, value, false, err
	}
	if !exact {
		at--
	}
	if at >= 0 {
		offset := indexPageHeader + at*leafRecordBytes
		matched := readKey(data, offset)
		copy(value[:], data[offset+8:offset+leafRecordBytes])
		s.release(slot, false)
		return matched, value, true, nil
	}
	s.release(slot, false)
	// If this leaf has no predecessor, take the immediately previous
	// ancestor child and descend its right edge. No leaf chain is walked.
	for path.n > 0 {
		path.n--
		step := path.steps[path.n]
		if step.child == 0 {
			continue
		}
		parent, pin, err := s.pin(step.page)
		if err != nil {
			return numericKey{}, value, false, err
		}
		count := int(read16(parent, 16))
		if parent[8] != pageBTreeBranch || parent[9] != tree || count == 0 || count > branchCapacity || step.child > count {
			s.release(pin, false)
			return numericKey{}, value, false, ErrIndexCorrupt
		}
		child := step.child - 1
		next := read32(parent, 24)
		if child > 0 {
			next = read32(parent, indexPageHeader+(child-1)*branchRecordBytes+8)
		}
		s.release(pin, false)
		if next == 0 || next >= s.nextPage {
			return numericKey{}, value, false, ErrIndexCorrupt
		}
		return s.rightmostNumeric(tree, next)
	}
	return numericKey{}, value, false, nil
}

func (s *numericTrees) rightmostNumeric(tree byte, page uint32) (numericKey, numericValue, bool, error) {
	var value numericValue
	for depth := 0; depth <= len((treePath{}).steps); depth++ {
		data, pin, err := s.pin(page)
		if err != nil {
			return numericKey{}, value, false, err
		}
		if data[9] != tree {
			s.release(pin, false)
			return numericKey{}, value, false, ErrIndexCorrupt
		}
		n := int(read16(data, 16))
		if data[8] == pageBTreeLeaf {
			if n == 0 || n > leafCapacity {
				s.release(pin, false)
				return numericKey{}, value, false, ErrIndexCorrupt
			}
			offset := indexPageHeader + (n-1)*leafRecordBytes
			key := readKey(data, offset)
			copy(value[:], data[offset+8:offset+leafRecordBytes])
			s.release(pin, false)
			return key, value, true, nil
		}
		if data[8] != pageBTreeBranch || n == 0 || n > branchCapacity {
			s.release(pin, false)
			return numericKey{}, value, false, ErrIndexCorrupt
		}
		next := read32(data, indexPageHeader+(n-1)*branchRecordBytes+8)
		s.release(pin, false)
		if next == 0 || next >= s.nextPage {
			return numericKey{}, value, false, ErrIndexCorrupt
		}
		page = next
	}
	return numericKey{}, value, false, ErrIndexInvariant
}
