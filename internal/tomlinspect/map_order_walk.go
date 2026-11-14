package tomlinspect

func (s *indexStore) firstMapName(pointer uint32) (uint32, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if pointer == 0 {
		return 0, nil
	}
	var previous uint32
	havePrevious := false
	for pointer&orderLeafFlag == 0 {
		node, err := s.readOrder(pointer)
		if err != nil {
			return 0, err
		}
		if havePrevious && node.bit <= previous {
			return 0, ErrIndexCorrupt
		}
		previous, havePrevious = node.bit, true
		pointer = node.left
	}
	return s.orderName(pointer)
}

func (s *indexStore) nextMapName(root, name uint32) (uint32, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if root&orderLeafFlag != 0 {
		if root&^orderLeafFlag != name {
			return 0, ErrIndexCorrupt
		}
		return 0, nil
	}
	incoming, err := s.readName(name)
	if err != nil {
		return 0, err
	}
	if incoming.kind != nameLeaf {
		return 0, ErrIndexCorrupt
	}
	cursor, err := (nameInput{text: incoming.text}).bitCursor()
	if err != nil {
		return 0, err
	}
	cursor.ctx = s.ctx
	pointer, right := root, uint32(0)
	var previous uint32
	havePrevious := false
	for pointer&orderLeafFlag == 0 {
		node, err := s.readOrder(pointer)
		if err != nil {
			return 0, err
		}
		if havePrevious && node.bit <= previous {
			return 0, ErrIndexCorrupt
		}
		previous, havePrevious = node.bit, true
		bit, past, err := cursor.at(node.bit)
		if err != nil {
			return 0, err
		}
		if past {
			return 0, ErrIndexCorrupt
		}
		if bit == 0 {
			right = node.right
			pointer = node.left
		} else {
			pointer = node.right
		}
	}
	if pointer&^orderLeafFlag != name {
		return 0, ErrIndexCorrupt
	}
	return s.firstMapName(right)
}

// mapEntries follows exact decoded UTF8 lexical order, matching the
// canonical configdata and P23 sorted-key traversals. It keeps one numeric
// successor state, not a decoded name slice or an input-depth Go stack.
func (s *indexStore) mapEntries(object uint32, visit func(uint32, storedValue) error) error {
	if visit == nil {
		return ErrIndexInvariant
	}
	info, err := s.object(object)
	if err != nil {
		return err
	}
	if info.kind != storedMap {
		return ErrIndexInvariant
	}
	name, err := s.firstMapName(info.order)
	if err != nil {
		return err
	}
	var count uint32
	for name != 0 {
		if count >= info.count {
			return ErrIndexCorrupt
		}
		value, found, err := s.mapValue(object, name)
		if err != nil {
			return err
		}
		if !found {
			return ErrIndexCorrupt
		}
		if err := visit(name, value); err != nil {
			return err
		}
		count++
		name, err = s.nextMapName(info.order, name)
		if err != nil {
			return err
		}
	}
	if count != info.count {
		return ErrIndexCorrupt
	}
	return nil
}
