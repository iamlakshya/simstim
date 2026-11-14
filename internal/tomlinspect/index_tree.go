package tomlinspect

const (
	treePaths byte = iota
	treeMaps
	treeArrays
	treeCount
)
const (
	leafRecordBytes   = 32
	branchRecordBytes = 16
	leafCapacity      = (indexPageBytes - indexPageHeader) / leafRecordBytes
	branchCapacity    = (indexPageBytes - indexPageHeader) / branchRecordBytes
)

type numericKey struct{ first, second uint32 }
type numericValue [24]byte
type branchStep struct {
	page  uint32
	child int
}
type treePath struct {
	steps [8]branchStep
	n     int
}

func compareKey(a, b numericKey) int {
	if a.first < b.first {
		return -1
	}
	if a.first > b.first {
		return 1
	}
	if a.second < b.second {
		return -1
	}
	if a.second > b.second {
		return 1
	}
	return 0
}
func readKey(data []byte, offset int) numericKey {
	return numericKey{read32(data, offset), read32(data, offset+4)}
}
func writeKey(data []byte, offset int, key numericKey) {
	write32(data, offset, key.first)
	write32(data, offset+4, key.second)
}

func (s *indexStore) leafPosition(data []byte, key numericKey) (int, bool, error) {
	n := int(read16(data, 16))
	if n > leafCapacity {
		return 0, false, ErrIndexCorrupt
	}
	lo, hi := 0, n
	for lo < hi {
		if err := s.ctx.Err(); err != nil {
			return 0, false, err
		}
		mid := (lo + hi) / 2
		if compareKey(readKey(data, indexPageHeader+mid*leafRecordBytes), key) < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, lo < n && compareKey(readKey(data, indexPageHeader+lo*leafRecordBytes), key) == 0, nil
}

func (s *indexStore) branchPosition(data []byte, key numericKey) (int, uint32, error) {
	n := int(read16(data, 16))
	if n == 0 || n > branchCapacity {
		return 0, 0, ErrIndexCorrupt
	}
	lo, hi := 0, n
	for lo < hi {
		if err := s.ctx.Err(); err != nil {
			return 0, 0, err
		}
		mid := (lo + hi) / 2
		if compareKey(key, readKey(data, indexPageHeader+mid*branchRecordBytes)) < 0 {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	if lo == 0 {
		return 0, read32(data, 24), nil
	}
	return lo, read32(data, indexPageHeader+(lo-1)*branchRecordBytes+8), nil
}

func (s *indexStore) descend(tree byte, key numericKey, path *treePath) (uint32, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if tree >= treeCount {
		return 0, ErrIndexInvariant
	}
	page := s.roots[tree]
	for page != 0 {
		data, slot, err := s.pin(page)
		if err != nil {
			return 0, err
		}
		if data[9] != tree {
			s.release(slot, false)
			return 0, ErrIndexCorrupt
		}
		if data[8] == pageBTreeLeaf {
			s.release(slot, false)
			return page, nil
		}
		if data[8] != pageBTreeBranch {
			s.release(slot, false)
			return 0, ErrIndexCorrupt
		}
		child, next, err := s.branchPosition(data, key)
		s.release(slot, false)
		if err != nil {
			return 0, err
		}
		if path != nil {
			if path.n == len(path.steps) {
				return 0, ErrIndexInvariant
			}
			path.steps[path.n] = branchStep{page: page, child: child}
			path.n++
		}
		if next == 0 || next >= s.nextPage {
			return 0, ErrIndexCorrupt
		}
		page = next
	}
	return 0, nil
}

func (s *indexStore) get(tree byte, key numericKey) (numericValue, bool, error) {
	var value numericValue
	s.counters.NumericLookups++
	page, err := s.descend(tree, key, nil)
	if err != nil || page == 0 {
		return value, false, err
	}
	data, slot, err := s.pin(page)
	if err != nil {
		return value, false, err
	}
	at, found, err := s.leafPosition(data, key)
	if err == nil && found {
		copy(value[:], data[indexPageHeader+at*leafRecordBytes+8:indexPageHeader+(at+1)*leafRecordBytes])
	}
	s.release(slot, false)
	return value, found, err
}

func insertLeaf(data []byte, at int, key numericKey, value numericValue) error {
	n := int(read16(data, 16))
	if n >= leafCapacity || at < 0 || at > n {
		return ErrIndexInvariant
	}
	offset := indexPageHeader + at*leafRecordBytes
	copy(data[offset+leafRecordBytes:indexPageHeader+(n+1)*leafRecordBytes], data[offset:indexPageHeader+n*leafRecordBytes])
	writeKey(data, offset, key)
	copy(data[offset+8:offset+leafRecordBytes], value[:])
	write16(data, 16, uint16(n+1))
	return nil
}

func insertBranch(data []byte, at int, key numericKey, right uint32) error {
	n := int(read16(data, 16))
	if n >= branchCapacity || at < 0 || at > n || right == 0 {
		return ErrIndexInvariant
	}
	offset := indexPageHeader + at*branchRecordBytes
	copy(data[offset+branchRecordBytes:indexPageHeader+(n+1)*branchRecordBytes], data[offset:indexPageHeader+n*branchRecordBytes])
	clear(data[offset : offset+branchRecordBytes])
	writeKey(data, offset, key)
	write32(data, offset+8, right)
	write16(data, 16, uint16(n+1))
	return nil
}
