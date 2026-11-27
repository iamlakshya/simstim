package tomlinspect

type objectInfo struct {
	kind           byte
	span           Span
	count, lastMap uint32
	order          uint32
}

func encodeObject(info objectInfo) [32]byte {
	var raw [32]byte
	raw[0] = info.kind
	write32(raw[:], 4, uint32(info.span.Start))
	write32(raw[:], 8, uint32(info.span.End))
	write32(raw[:], 12, info.count)
	write32(raw[:], 16, info.lastMap)
	write32(raw[:], 20, info.order)
	return raw
}
func (s *indexStore) object(id uint32) (objectInfo, error) {
	var raw [32]byte
	if err := s.readRecord(recordObjects, id, raw[:]); err != nil {
		return objectInfo{}, err
	}
	info := objectInfo{kind: raw[0], span: Span{Start: int64(read32(raw[:], 4)), End: int64(read32(raw[:], 8))}, count: read32(raw[:], 12), lastMap: read32(raw[:], 16), order: read32(raw[:], 20)}
	if info.kind != storedMap && info.kind != storedArray && info.kind != storedTableArray {
		return objectInfo{}, ErrIndexCorrupt
	}
	if info.span.Start < 0 || info.span.Start > s.textSource.size || info.span.End > s.textSource.size {
		return objectInfo{}, ErrIndexCorrupt
	}
	if info.kind == storedTableArray && info.count != 0 && (info.lastMap == 0 || info.lastMap > s.classes[recordObjects].count) {
		return objectInfo{}, ErrIndexCorrupt
	}
	return info, nil
}
func (s *indexStore) writeObject(id uint32, info objectInfo) error {
	raw := encodeObject(info)
	return s.writeRecord(recordObjects, id, raw[:])
}
func (s *indexStore) newObject(kind byte, start int64) (storedValue, error) {
	if kind != storedMap && kind != storedArray && kind != storedTableArray || start < 0 || start > s.textSource.size {
		return storedValue{}, ErrIndexInvariant
	}
	info := objectInfo{kind: kind, span: Span{Start: start}}
	raw := encodeObject(info)
	id, err := s.appendRecord(recordObjects, raw[:])
	return storedValue{kind: kind, object: id, span: info.span}, err
}
func (s *indexStore) finishObject(value storedValue, end int64) (storedValue, error) {
	info, err := s.object(value.object)
	if err != nil {
		return storedValue{}, err
	}
	if info.kind != value.kind || end < info.span.Start || end > s.textSource.size {
		return storedValue{}, ErrIndexInvariant
	}
	info.span.End = end
	if err := s.writeObject(value.object, info); err != nil {
		return storedValue{}, err
	}
	value.span = info.span
	return value, nil
}

func (s *indexStore) mapValue(object, name uint32) (storedValue, bool, error) {
	info, err := s.object(object)
	if err != nil {
		return storedValue{}, false, err
	}
	if info.kind != storedMap || name == 0 || name > s.classes[recordNames].count {
		return storedValue{}, false, ErrIndexInvariant
	}
	raw, found, err := s.get(treeMaps, numericKey{object, name})
	if err != nil || !found {
		return storedValue{}, found, err
	}
	value, err := s.decodeValue(raw)
	return value, true, err
}
func (s *indexStore) setMapValue(object, name uint32, value storedValue) error {
	info, err := s.object(object)
	if err != nil {
		return err
	}
	if info.kind != storedMap || name == 0 || name > s.classes[recordNames].count {
		return ErrIndexInvariant
	}
	key := numericKey{object, name}
	_, found, err := s.get(treeMaps, key)
	if err != nil {
		return err
	}
	if !found {
		if info.count == ^uint32(0) {
			return ErrIndexInvariant
		}
		info.order, err = s.insertMapOrder(info.order, name)
		if err != nil {
			return err
		}
		info.count++
	}
	if err := s.put(treeMaps, key, encodeValue(value)); err != nil {
		return err
	}
	if !found {
		return s.writeObject(object, info)
	}
	return nil
}
func (s *indexStore) appendArray(array uint32, value storedValue) error {
	info, err := s.object(array)
	if err != nil {
		return err
	}
	if info.kind != storedArray && info.kind != storedTableArray || info.kind == storedTableArray && value.kind != storedMap || info.count == ^uint32(0) {
		return ErrIndexInvariant
	}
	if err := s.put(treeArrays, numericKey{array, info.count}, encodeValue(value)); err != nil {
		return err
	}
	info.count++
	if info.kind == storedTableArray {
		info.lastMap = value.object
	}
	return s.writeObject(array, info)
}
func (s *indexStore) arrayValues(array uint32, visit func(uint32, storedValue) error) error {
	info, err := s.object(array)
	if err != nil {
		return err
	}
	if info.kind != storedArray && info.kind != storedTableArray || visit == nil {
		return ErrIndexInvariant
	}
	var index uint32
	err = s.walk(treeArrays, array, func(key numericKey, raw numericValue) error {
		if key.second != index || index >= info.count {
			return ErrIndexCorrupt
		}
		value, err := s.decodeValue(raw)
		if err != nil {
			return err
		}
		if info.kind == storedTableArray && value.kind != storedMap {
			return ErrIndexCorrupt
		}
		if err := visit(index, value); err != nil {
			return err
		}
		index++
		return nil
	})
	if err == nil && index != info.count {
		return ErrIndexCorrupt
	}
	return err
}
