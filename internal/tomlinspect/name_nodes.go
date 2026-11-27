package tomlinspect

const (
	nameLeaf byte = iota + 1
	nameBranch
)

type nameNode struct {
	kind                             byte
	text                             Text
	bit, left, right, representative uint32
}

func (s *indexStore) readName(id uint32) (nameNode, error) {
	var raw [32]byte
	if err := s.readRecord(recordNames, id, raw[:]); err != nil {
		return nameNode{}, err
	}
	s.counters.TrieVisits++
	out := nameNode{kind: raw[0]}
	switch out.kind {
	case nameLeaf:
		kind := itemType(raw[1])
		switch kind {
		case itemText, itemString, itemStringEsc, itemRawString, itemMultilineString, itemRawMultilineString:
		default:
			return nameNode{}, ErrIndexCorrupt
		}
		out.text = Text{source: s.textSource, span: Span{Start: int64(read32(raw[:], 4)), End: int64(read32(raw[:], 8))}, kind: kind, generation: s.textSource.generation}
		if err := s.textSource.spanValid(out.text.span); err != nil {
			return nameNode{}, err
		}
		out.representative = id
	case nameBranch:
		out.bit, out.left, out.right, out.representative = read32(raw[:], 4), read32(raw[:], 8), read32(raw[:], 12), read32(raw[:], 16)
		count := s.classes[recordNames].count
		if out.bit > uint32(9*s.textSource.size+1) || out.left == 0 || out.right == 0 || out.representative == 0 || out.left > count || out.right > count || out.representative > count {
			return nameNode{}, ErrIndexCorrupt
		}
	default:
		return nameNode{}, ErrIndexCorrupt
	}
	return out, nil
}

func encodeName(node nameNode) [32]byte {
	var raw [32]byte
	raw[0] = node.kind
	if node.kind == nameLeaf {
		raw[1] = byte(node.text.kind)
		write32(raw[:], 4, uint32(node.text.span.Start))
		write32(raw[:], 8, uint32(node.text.span.End))
	} else {
		write32(raw[:], 4, node.bit)
		write32(raw[:], 8, node.left)
		write32(raw[:], 12, node.right)
		write32(raw[:], 16, node.representative)
	}
	return raw
}

func (s *indexStore) appendName(node nameNode) (uint32, error) {
	raw := encodeName(node)
	return s.appendRecord(recordNames, raw[:])
}
func (s *indexStore) writeName(id uint32, node nameNode) error {
	raw := encodeName(node)
	return s.writeRecord(recordNames, id, raw[:])
}
