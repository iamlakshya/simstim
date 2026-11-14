package tomlinspect

func (s *numericTrees) put(tree byte, key numericKey, value numericValue) error {
	if tree >= treeCount {
		return ErrIndexInvariant
	}
	if s.roots[tree] == 0 {
		id, data, slot, err := s.allocate(pageBTreeLeaf, tree)
		if err != nil {
			return err
		}
		err = insertLeaf(data, 0, key, value)
		s.release(slot, true)
		if err == nil {
			s.roots[tree] = id
		}
		return err
	}
	var path treePath
	page, err := s.descend(tree, key, &path)
	if err != nil {
		return err
	}
	data, slot, err := s.pin(page)
	if err != nil {
		return err
	}
	at, found, err := s.leafPosition(data, key)
	if err != nil {
		s.release(slot, false)
		return err
	}
	if found {
		offset := indexPageHeader + at*leafRecordBytes + 8
		copy(data[offset:offset+len(value)], value[:])
		s.release(slot, true)
		return nil
	}
	if int(read16(data, 16)) < leafCapacity {
		err = insertLeaf(data, at, key, value)
		s.release(slot, true)
		return err
	}
	separator, right, err := s.splitLeaf(tree, data, slot, key, value)
	if err != nil {
		return err
	}
	for path.n > 0 {
		path.n--
		step := path.steps[path.n]
		parent, parentSlot, err := s.pin(step.page)
		if err != nil {
			return err
		}
		if parent[8] != pageBTreeBranch || parent[9] != tree {
			s.release(parentSlot, false)
			return ErrIndexCorrupt
		}
		if int(read16(parent, 16)) < branchCapacity {
			err = insertBranch(parent, step.child, separator, right)
			s.release(parentSlot, true)
			return err
		}
		separator, right, err = s.splitBranch(tree, parent, parentSlot, step.child, separator, right)
		if err != nil {
			return err
		}
	}
	root, rootData, rootSlot, err := s.allocate(pageBTreeBranch, tree)
	if err != nil {
		return err
	}
	write32(rootData, 24, s.roots[tree])
	err = insertBranch(rootData, 0, separator, right)
	s.release(rootSlot, true)
	if err == nil {
		s.roots[tree] = root
	}
	return err
}

// splitLeaf owns and releases oldSlot on every path. All moved records are
// fixed metadata references; no decoded input is copied into page storage.
func (s *numericTrees) splitLeaf(tree byte, old []byte, oldSlot int, key numericKey, value numericValue) (numericKey, uint32, error) {
	mid := leafCapacity/2 + 1
	separator := readKey(old, indexPageHeader+mid*leafRecordBytes)
	id, next, nextSlot, err := s.allocate(pageBTreeLeaf, tree)
	if err != nil {
		s.release(oldSlot, false)
		return numericKey{}, 0, err
	}
	count := leafCapacity - mid
	copy(next[indexPageHeader:indexPageHeader+count*leafRecordBytes], old[indexPageHeader+mid*leafRecordBytes:indexPageHeader+leafCapacity*leafRecordBytes])
	write16(next, 16, uint16(count))
	write32(next, 20, read32(old, 20))
	write16(old, 16, uint16(mid))
	write32(old, 20, id)
	order, err := s.order(key, separator)
	if err != nil {
		s.release(oldSlot, true)
		s.release(nextSlot, true)
		return numericKey{}, 0, err
	}
	if order < 0 {
		at, _, failure := s.leafPosition(old, key)
		if failure == nil {
			failure = insertLeaf(old, at, key, value)
		}
		err = failure
	} else {
		at, _, failure := s.leafPosition(next, key)
		if failure == nil {
			failure = insertLeaf(next, at, key, value)
		}
		err = failure
	}
	s.release(oldSlot, true)
	s.release(nextSlot, true)
	if err != nil {
		return numericKey{}, 0, err
	}
	s.counters.Splits++
	return separator, id, nil
}

func (s *numericTrees) splitBranch(tree byte, old []byte, oldSlot, at int, key numericKey, right uint32) (numericKey, uint32, error) {
	mid := branchCapacity / 2
	separator := readKey(old, indexPageHeader+mid*branchRecordBytes)
	id, next, nextSlot, err := s.allocate(pageBTreeBranch, tree)
	if err != nil {
		s.release(oldSlot, false)
		return numericKey{}, 0, err
	}
	write32(next, 24, read32(old, indexPageHeader+mid*branchRecordBytes+8))
	count := branchCapacity - mid - 1
	copy(next[indexPageHeader:indexPageHeader+count*branchRecordBytes], old[indexPageHeader+(mid+1)*branchRecordBytes:indexPageHeader+branchCapacity*branchRecordBytes])
	write16(next, 16, uint16(count))
	write16(old, 16, uint16(mid))
	if at <= mid {
		err = insertBranch(old, at, key, right)
	} else {
		err = insertBranch(next, at-mid-1, key, right)
	}
	s.release(oldSlot, true)
	s.release(nextSlot, true)
	if err != nil {
		return numericKey{}, 0, err
	}
	s.counters.Splits++
	return separator, id, nil
}
