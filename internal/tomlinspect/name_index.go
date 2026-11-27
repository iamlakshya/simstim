package tomlinspect

func (s *indexStore) nameRepresentative(query nameInput) (uint32, error) {
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	if s.nameRoot == 0 {
		return 0, nil
	}
	cursor, err := query.bitCursor()
	if err != nil {
		return 0, err
	}
	cursor.ctx = s.ctx
	id := s.nameRoot
	var previous uint32
	havePrevious := false
	for {
		if err := s.ctx.Err(); err != nil {
			return 0, err
		}
		node, err := s.readName(id)
		if err != nil {
			return 0, err
		}
		if node.kind == nameLeaf {
			return id, nil
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
			representative, err := s.readName(node.representative)
			if err != nil {
				return 0, err
			}
			if representative.kind != nameLeaf {
				return 0, ErrIndexCorrupt
			}
			return node.representative, nil
		}
		id = node.left
		if bit != 0 {
			id = node.right
		}
	}
}

func (s *indexStore) internName(text Text) (uint32, error) {
	if text.source != s.textSource {
		return 0, ErrIndexInvariant
	}
	if err := text.Walk(func(rune) error { return nil }); err != nil {
		return 0, err
	}
	query := nameInput{text: text}
	representative, err := s.nameRepresentative(query)
	if err != nil {
		return 0, err
	}
	if representative == 0 {
		id, err := s.appendName(nameNode{kind: nameLeaf, text: text})
		if err == nil {
			s.nameRoot = id
		}
		return id, err
	}
	leaf, err := s.readName(representative)
	if err != nil {
		return 0, err
	}
	equal, differing, bit, err := s.compareNames(query, leaf.text)
	if err != nil {
		return 0, err
	}
	if equal {
		return representative, nil
	}
	newLeaf, err := s.appendName(nameNode{kind: nameLeaf, text: text})
	if err != nil {
		return 0, err
	}
	cursor, err := query.bitCursor()
	if err != nil {
		return 0, err
	}
	cursor.ctx = s.ctx
	id, parent := s.nameRoot, uint32(0)
	parentBit := byte(0)
	var previous uint32
	havePrevious := false
	var node nameNode
	for {
		node, err = s.readName(id)
		if err != nil {
			return 0, err
		}
		if node.kind == nameLeaf || node.bit >= differing {
			break
		}
		if havePrevious && node.bit <= previous {
			return 0, ErrIndexCorrupt
		}
		previous, havePrevious = node.bit, true
		choice, past, err := cursor.at(node.bit)
		if err != nil {
			return 0, err
		}
		if past {
			return 0, ErrIndexInvariant
		}
		parent, parentBit = id, choice
		id = node.left
		if choice != 0 {
			id = node.right
		}
	}
	if node.kind == nameBranch && node.bit == differing {
		return 0, ErrIndexInvariant
	}
	branch := nameNode{kind: nameBranch, bit: differing, left: id, right: newLeaf, representative: node.representative}
	if bit == 0 {
		branch.left, branch.right = newLeaf, id
	}
	newBranch, err := s.appendName(branch)
	if err != nil {
		return 0, err
	}
	if parent == 0 {
		s.nameRoot = newBranch
		return newLeaf, nil
	}
	outer, err := s.readName(parent)
	if err != nil {
		return 0, err
	}
	if outer.kind != nameBranch {
		return 0, ErrIndexCorrupt
	}
	if parentBit == 0 {
		if outer.left != id {
			return 0, ErrIndexInvariant
		}
		outer.left = newBranch
	} else {
		if outer.right != id {
			return 0, ErrIndexInvariant
		}
		outer.right = newBranch
	}
	if err := s.writeName(parent, outer); err != nil {
		return 0, err
	}
	return newLeaf, nil
}

// lookupLiteral only queries a code-owned canonical name. It never inserts
// a static string or decoded user content into the file.
func (s *indexStore) lookupLiteral(literal string) (uint32, bool, error) {
	query := nameInput{literal: literal, static: true}
	id, err := s.nameRepresentative(query)
	if err != nil || id == 0 {
		return 0, false, err
	}
	leaf, err := s.readName(id)
	if err != nil {
		return 0, false, err
	}
	equal, _, _, err := s.compareNames(query, leaf.text)
	return id, equal, err
}
