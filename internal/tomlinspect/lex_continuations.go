package tomlinspect

const (
	contNone byte = iota
	contLexArrayTableEnd
	contLexArrayValue
	contLexArrayValueEnd
	contLexInlineTableValue
	contLexInlineTableValueEnd
	contLexKeyEnd
	contLexMultilineString
	contLexString
	contLexTableEnd
	contLexTableNameEnd
	contLexTop
	contLexTopEnd
)

// Resolve at runtime: a global function-value table creates a Go
// initialisation dependency cycle through the preserved grammar and pop.
func continuationState(code byte) stateFn {
	switch code {
	case contNone:
		return nil
	case contLexArrayTableEnd:
		return lexArrayTableEnd
	case contLexArrayValue:
		return lexArrayValue
	case contLexArrayValueEnd:
		return lexArrayValueEnd
	case contLexInlineTableValue:
		return lexInlineTableValue
	case contLexInlineTableValueEnd:
		return lexInlineTableValueEnd
	case contLexKeyEnd:
		return lexKeyEnd
	case contLexMultilineString:
		return lexMultilineString
	case contLexString:
		return lexString
	case contLexTableEnd:
		return lexTableEnd
	case contLexTableNameEnd:
		return lexTableNameEnd
	case contLexTop:
		return lexTop
	case contLexTopEnd:
		return lexTopEnd
	default:
		panic("pinned lexer continuation invariant")
	}
}

func (lx *lexer) push(code byte) {
	if !lx.tiny {
		lx.arena.push(code)
		return
	}
	if lx.tinyDepth == len(lx.tinyData)*2 {
		panic("pinned flat-key continuation invariant exceeded")
	}
	index, shift := lx.tinyDepth/2, uint(lx.tinyDepth%2)*4
	lx.tinyData[index] = lx.tinyData[index]&^(15<<shift) | code<<shift
	lx.tinyDepth++
}

func (lx *lexer) pop() stateFn {
	if !lx.tiny {
		return continuationState(lx.arena.pop())
	}
	code := lx.peekContinuation()
	if code == contNone {
		panic("pinned flat-key has no continuation")
	}
	lx.tinyDepth--
	return continuationState(code)
}

func (lx *lexer) peekContinuation() byte {
	if !lx.tiny {
		return lx.arena.peek()
	}
	if lx.tinyDepth == 0 {
		return contNone
	}
	depth := lx.tinyDepth - 1
	return lx.tinyData[depth/2] >> (uint(depth%2) * 4) & 15
}
