package tomlinspect

const orderLeafFlag uint32 = 1 << 31

type orderBranch struct{ bit, left, right, representative uint32 }

func (s *indexStore) readOrder(id uint32) (orderBranch, error) {
	if id == 0 || id&orderLeafFlag != 0 {
		return orderBranch{}, ErrIndexCorrupt
	}
	var raw [16]byte
	if err := s.readRecord(recordMapOrder, id, raw[:]); err != nil {
		return orderBranch{}, err
	}
	node := orderBranch{read32(raw[:], 0), read32(raw[:], 4), read32(raw[:], 8), read32(raw[:], 12)}
	if node.bit > uint32(9*s.textSource.size+1) || node.left == 0 || node.right == 0 || node.representative == 0 || node.representative > s.classes[recordNames].count {
		return orderBranch{}, ErrIndexCorrupt
	}
	return node, nil
}
func encodeOrder(node orderBranch) [16]byte {
	var raw [16]byte
	write32(raw[:], 0, node.bit)
	write32(raw[:], 4, node.left)
	write32(raw[:], 8, node.right)
	write32(raw[:], 12, node.representative)
	return raw
}
func (s *indexStore) orderName(pointer uint32) (uint32, error) {
	if pointer&orderLeafFlag == 0 {
		return 0, ErrIndexCorrupt
	}
	id := pointer &^ orderLeafFlag
	name, err := s.readName(id)
	if err != nil {
		return 0, err
	}
	if name.kind != nameLeaf {
		return 0, ErrIndexCorrupt
	}
	return id, nil
}
func (s *indexStore) orderRepresentative(root uint32, query nameInput) (uint32, error) {
	cursor, err := query.bitCursor()
	if err != nil {
		return 0, err
	}
	cursor.ctx = s.ctx
	pointer := root
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
			return node.representative, nil
		}
		pointer = node.left
		if bit != 0 {
			pointer = node.right
		}
	}
	return s.orderName(pointer)
}

// Only a NEW key needs the order index. Existing numeric-key updates never
// recompare a giant current key for every array element or implicit write.
func (s *indexStore) insertMapOrder(root, name uint32) (uint32, error) {
	if name == 0 || name&orderLeafFlag != 0 {
		return 0, ErrIndexInvariant
	}
	if root == 0 {
		return orderLeafFlag | name, nil
	}
	incoming, err := s.readName(name)
	if err != nil {
		return 0, err
	}
	if incoming.kind != nameLeaf {
		return 0, ErrIndexCorrupt
	}
	query := nameInput{text: incoming.text}
	representative, err := s.orderRepresentative(root, query)
	if err != nil {
		return 0, err
	}
	stored, err := s.readName(representative)
	if err != nil {
		return 0, err
	}
	equal, differing, bit, err := s.compareNames(query, stored.text)
	if err != nil {
		return 0, err
	}
	if equal {
		return 0, ErrIndexInvariant
	}
	cursor, err := query.bitCursor()
	if err != nil {
		return 0, err
	}
	cursor.ctx = s.ctx
	pointer, parent := root, uint32(0)
	choice := byte(0)
	var previous uint32
	havePrevious := false
	var current orderBranch
	for pointer&orderLeafFlag == 0 {
		current, err = s.readOrder(pointer)
		if err != nil {
			return 0, err
		}
		if current.bit >= differing {
			break
		}
		if havePrevious && current.bit <= previous {
			return 0, ErrIndexCorrupt
		}
		previous, havePrevious = current.bit, true
		selected, past, err := cursor.at(current.bit)
		if err != nil {
			return 0, err
		}
		if past {
			return 0, ErrIndexInvariant
		}
		parent, choice = pointer, selected
		pointer = current.left
		if selected != 0 {
			pointer = current.right
		}
	}
	if pointer&orderLeafFlag == 0 && current.bit == differing {
		return 0, ErrIndexInvariant
	}
	rep := current.representative
	if pointer&orderLeafFlag != 0 {
		rep = pointer &^ orderLeafFlag
	}
	node := orderBranch{bit: differing, left: pointer, right: orderLeafFlag | name, representative: rep}
	if bit == 0 {
		node.left, node.right = orderLeafFlag|name, pointer
	}
	raw := encodeOrder(node)
	created, err := s.appendRecord(recordMapOrder, raw[:])
	if err != nil {
		return 0, err
	}
	if created&orderLeafFlag != 0 {
		return 0, ErrIndexInvariant
	}
	if parent == 0 {
		return created, nil
	}
	outer, err := s.readOrder(parent)
	if err != nil {
		return 0, err
	}
	if choice == 0 {
		if outer.left != pointer {
			return 0, ErrIndexInvariant
		}
		outer.left = created
	} else {
		if outer.right != pointer {
			return 0, ErrIndexInvariant
		}
		outer.right = created
	}
	raw = encodeOrder(outer)
	if err := s.writeRecord(recordMapOrder, parent, raw[:]); err != nil {
		return 0, err
	}
	return root, nil
}
