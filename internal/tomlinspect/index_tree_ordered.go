package tomlinspect

// leftmost follows physical first-child links. A semantic comparison has no
// numeric lower-bound sentinel, so it must never be called with an invented
// key just to start an ordered traversal.
func (s *numericTrees) leftmost(tree byte) (uint32, error) {
	if tree >= treeCount {
		return 0, ErrIndexInvariant
	}
	page := s.roots[tree]
	for depth := 0; page != 0; depth++ {
		if err := s.ctx.Err(); err != nil {
			return 0, err
		}
		if depth > len((treePath{}).steps) {
			return 0, ErrIndexCorrupt
		}
		data, slot, err := s.pin(page)
		if err != nil {
			return 0, err
		}
		kind, class, count := data[8], data[9], int(read16(data, 16))
		next := read32(data, 24)
		s.release(slot, false)
		if class != tree || count == 0 {
			return 0, ErrIndexCorrupt
		}
		if kind == pageBTreeLeaf {
			if count > leafCapacity {
				return 0, ErrIndexCorrupt
			}
			return page, nil
		}
		if kind != pageBTreeBranch || count > branchCapacity || next == 0 || next >= s.nextPage {
			return 0, ErrIndexCorrupt
		}
		page = next
	}
	return 0, nil
}

func (s *numericTrees) walkOrdered(tree byte, visit func(numericKey, numericValue) error) error {
	if visit == nil {
		return ErrIndexInvariant
	}
	page, err := s.leftmost(tree)
	if err != nil {
		return err
	}
	var previous numericKey
	havePrevious := false
	var pagesSeen uint32
	for page != 0 {
		pagesSeen++
		if pagesSeen >= s.nextPage {
			return ErrIndexCorrupt
		}
		at := 0
		for {
			if err := s.ctx.Err(); err != nil {
				return err
			}
			data, slot, err := s.pin(page)
			if err != nil {
				return err
			}
			n := int(read16(data, 16))
			if data[8] != pageBTreeLeaf || data[9] != tree || n == 0 || n > leafCapacity {
				s.release(slot, false)
				return ErrIndexCorrupt
			}
			if at == n {
				next := read32(data, 20)
				s.release(slot, false)
				if next >= s.nextPage {
					return ErrIndexCorrupt
				}
				page = next
				break
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
			// No cache pin crosses either a comparison or a visitor. Both
			// may read fixed records from this same bounded page store.
			if err := visit(key, value); err != nil {
				return err
			}
			at++
		}
	}
	return nil
}
