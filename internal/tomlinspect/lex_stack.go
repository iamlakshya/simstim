package tomlinspect

const lexerWorkspaceBytes = 256 << 10

// Recursive continuations have only two forms. Their ONE bit per push
// covers unbalanced MaxInput prefixes too; no matched closing delimiter is
// assumed. Non-recursive states need at most three simultaneous markers in
// the pinned grammar (root/header, quoted key, escape); four packed words
// retain their exact bit-stack positions within16 fixed bytes.
type lexStack struct {
	data                 []byte
	markers              [4]uint32
	markerCount          int
	bits, depth, maximum int
}

func (s *lexStack) reset(slab []byte) error {
	if len(slab) != IndexWorkspaceBytes {
		return ErrIndexInvariant
	}
	*s = lexStack{data: slab[lexerWorkspaceBytes:]}
	return nil
}

func (s *lexStack) push(code byte) {
	if code == 0 || code > 15 {
		panic("pinned lexer continuation invariant")
	}
	if code == contLexArrayValueEnd || code == contLexInlineTableValueEnd {
		if s.bits/8 >= len(s.data) {
			panic("pinned lexer prefix-source charge invariant exceeded")
		}
		index, shift := len(s.data)-1-s.bits/8, uint(s.bits%8)
		bit := byte(0)
		if code == contLexInlineTableValueEnd {
			bit = 1
		}
		s.data[index] = s.data[index]&^(1<<shift) | bit<<shift
		s.bits++
	} else {
		if s.markerCount == len(s.markers) {
			panic("pinned transient continuation invariant exceeded")
		}
		s.markers[s.markerCount] = uint32(s.bits)<<4 | uint32(code)
		s.markerCount++
	}
	s.depth++
	s.maximum = max(s.maximum, s.depth)
}

func (s *lexStack) marker() bool {
	return s.markerCount != 0 && int(s.markers[s.markerCount-1]>>4) == s.bits
}
func (s *lexStack) pop() byte {
	if s.depth == 0 {
		panic("pinned lexer has no continuation")
	}
	s.depth--
	if s.marker() {
		s.markerCount--
		code := byte(s.markers[s.markerCount] & 15)
		s.markers[s.markerCount] = 0
		return code
	}
	if s.bits == 0 {
		panic("pinned binary continuation invariant")
	}
	s.bits--
	if s.data[len(s.data)-1-s.bits/8]>>uint(s.bits%8)&1 != 0 {
		return contLexInlineTableValueEnd
	}
	return contLexArrayValueEnd
}
func (s *lexStack) peek() byte {
	if s.depth == 0 {
		return contNone
	}
	if s.marker() {
		return byte(s.markers[s.markerCount-1] & 15)
	}
	if s.bits == 0 {
		panic("pinned binary continuation invariant")
	}
	index := s.bits - 1
	if s.data[len(s.data)-1-index/8]>>uint(index%8)&1 != 0 {
		return contLexInlineTableValueEnd
	}
	return contLexArrayValueEnd
}
