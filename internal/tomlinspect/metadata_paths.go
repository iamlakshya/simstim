package tomlinspect

const (
	pathUnknown byte = iota
	pathString
	pathBool
	pathInteger
	pathFloat
	pathDatetime
	pathArray
	pathTable
	pathTableArray
)

type pathInfo struct {
	parent, name, depth uint32
	kind                byte
	implicit            bool
	position            Position
}

func encodePath(info pathInfo) [32]byte {
	var raw [32]byte
	write32(raw[:], 0, info.parent)
	write32(raw[:], 4, info.name)
	write32(raw[:], 8, info.depth)
	raw[12] = info.kind
	if info.implicit {
		raw[13] = 1
	}
	write32(raw[:], 16, uint32(info.position.Line))
	write32(raw[:], 20, uint32(info.position.Start))
	write32(raw[:], 24, uint32(info.position.Len))
	return raw
}
func (s *indexStore) path(id uint32) (pathInfo, error) {
	if err := s.ctx.Err(); err != nil {
		return pathInfo{}, err
	}
	if id == 0 {
		return pathInfo{kind: pathTable}, nil
	}
	var raw [32]byte
	if err := s.readRecord(recordPaths, id, raw[:]); err != nil {
		return pathInfo{}, err
	}
	info := pathInfo{parent: read32(raw[:], 0), name: read32(raw[:], 4), depth: read32(raw[:], 8), kind: raw[12], implicit: raw[13] != 0, position: Position{Line: int(read32(raw[:], 16)), Start: int(read32(raw[:], 20)), Len: int(read32(raw[:], 24))}}
	if info.parent > s.classes[recordPaths].count || info.name == 0 || info.name > s.classes[recordNames].count || info.depth == 0 || info.kind > pathTableArray || raw[13] > 1 {
		return pathInfo{}, ErrIndexCorrupt
	}
	return info, nil
}
func (s *indexStore) writePath(id uint32, info pathInfo) error {
	if id == 0 {
		return ErrIndexInvariant
	}
	raw := encodePath(info)
	return s.writeRecord(recordPaths, id, raw[:])
}
func (s *indexStore) internPath(parent, name uint32) (uint32, error) {
	key := numericKey{parent, name}
	raw, found, err := s.get(treePaths, key)
	if err != nil {
		return 0, err
	}
	if found {
		id := read32(raw[:], 0)
		info, err := s.path(id)
		if err != nil {
			return 0, err
		}
		if info.parent != parent || info.name != name {
			return 0, ErrIndexCorrupt
		}
		return id, nil
	}
	outer, err := s.path(parent)
	if err != nil {
		return 0, err
	}
	if outer.depth == ^uint32(0) || name == 0 || name > s.classes[recordNames].count {
		return 0, ErrIndexInvariant
	}
	info := pathInfo{parent: parent, name: name, depth: outer.depth + 1}
	encoded := encodePath(info)
	id, err := s.appendRecord(recordPaths, encoded[:])
	if err != nil {
		return 0, err
	}
	var value numericValue
	write32(value[:], 0, id)
	if err := s.put(treePaths, key, value); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *indexStore) lookupPath(parent, name uint32) (uint32, error) {
	raw, found, err := s.get(treePaths, numericKey{parent, name})
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, ErrIndexCorrupt
	}
	id := read32(raw[:], 0)
	info, err := s.path(id)
	if err != nil {
		return 0, err
	}
	if info.parent != parent || info.name != name {
		return 0, ErrIndexCorrupt
	}
	return id, nil
}
func (s *indexStore) implicit(path uint32, present bool) error {
	info, err := s.path(path)
	if err != nil {
		return err
	}
	info.implicit = present
	return s.writePath(path, info)
}
func (s *indexStore) typeAt(path uint32, kind byte, pos Position) error {
	info, err := s.path(path)
	if err != nil {
		return err
	}
	if kind > pathTableArray {
		return ErrIndexInvariant
	}
	info.kind, info.position = kind, pos
	return s.writePath(path, info)
}
