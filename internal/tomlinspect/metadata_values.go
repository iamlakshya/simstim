package tomlinspect

const (
	storedMissing byte = iota
	storedScalar
	storedMap
	storedArray
	storedTableArray
)

type storedValue struct {
	kind   byte
	token  itemType
	object uint32
	span   Span
}

func encodeValue(value storedValue) numericValue {
	var raw numericValue
	raw[0], raw[1] = value.kind, byte(value.token)
	write32(raw[:], 4, value.object)
	write32(raw[:], 8, uint32(value.span.Start))
	write32(raw[:], 12, uint32(value.span.End))
	return raw
}

func (s *indexStore) decodeValue(raw numericValue) (storedValue, error) {
	value := storedValue{kind: raw[0], token: itemType(raw[1]), object: read32(raw[:], 4), span: Span{Start: int64(read32(raw[:], 8)), End: int64(read32(raw[:], 12))}}
	switch value.kind {
	case storedScalar:
		switch value.token {
		case itemString, itemStringEsc, itemRawString, itemMultilineString, itemRawMultilineString, itemBool, itemInteger, itemFloat, itemDatetime:
		default:
			return storedValue{}, ErrIndexCorrupt
		}
		if value.object != 0 {
			return storedValue{}, ErrIndexCorrupt
		}
		if err := s.textSource.spanValid(value.span); err != nil {
			return storedValue{}, err
		}
	case storedMap, storedArray, storedTableArray:
		if value.object == 0 || value.object > s.classes[recordObjects].count {
			return storedValue{}, ErrIndexCorrupt
		}
	default:
		return storedValue{}, ErrIndexCorrupt
	}
	return value, nil
}
