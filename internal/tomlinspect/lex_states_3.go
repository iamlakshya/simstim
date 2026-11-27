// Derived from github.com/BurntSushi/toml v1.6.0 lex.go.
// Original MIT licence is in COPYING. Grammar functions are preserved.
package tomlinspect

import (
	"unicode"
)

func lexBaseNumberOrDate(lx *lexer) stateFn {
	r := lx.next()
	// Note: All datetimes start with at least two digits, so we don't
	// handle date characters (':', '-', etc.) here.
	if isDigit(r) {
		return lexNumberOrDate
	}
	switch r {
	case '_':
		// Can only be decimal, because there can't be an underscore
		// between the '0' and the base designator, and dates can't
		// contain underscores.
		return lexDecimalNumber
	case '.', 'e', 'E':
		return lexFloat
	case 'b':
		r = lx.peek()
		if !isBinary(r) {
			lx.errorf("not a binary number: '%s%c'", redactedSpanDiagnostic(lx.current()), r)
		}
		return lexBinaryInteger
	case 'o':
		r = lx.peek()
		if !isOctal(r) {
			lx.errorf("not an octal number: '%s%c'", redactedSpanDiagnostic(lx.current()), r)
		}
		return lexOctalInteger
	case 'x':
		r = lx.peek()
		if !isHex(r) {
			lx.errorf("not a hexadecimal number: '%s%c'", redactedSpanDiagnostic(lx.current()), r)
		}
		return lexHexInteger
	}

	lx.backup()
	lx.emit(itemInteger)
	return lx.pop()
}

// lexFloat consumes the elements of a float. It allows any sequence of
// float-like characters, so floats emitted by the lexer are only a first
// approximation and must be validated by the parser.
func lexFloat(lx *lexer) stateFn {
	r := lx.next()
	if isDigit(r) {
		return lexFloat
	}
	switch r {
	case '_', '.', '-', '+', 'e', 'E':
		return lexFloat
	}

	lx.backup()
	lx.emit(itemFloat)
	return lx.pop()
}

// lexBool consumes a bool string: 'true' or 'false.
func lexBool(lx *lexer) stateFn {
	// The pin accepts exactly these two words after scanning all letters.
	// Keep that scan, including invalid arbitrarily long Unicode words,
	// without retaining their bytes in the lexer or its error.
	count, isTrue, isFalse := 0, true, true
	for {
		r := lx.next()
		if !unicode.IsLetter(r) {
			lx.backup()
			break
		}
		isTrue = isTrue && count < len("true") && r == rune("true"[count])
		isFalse = isFalse && count < len("false") && r == rune("false"[count])
		count++
	}
	if isTrue && count == len("true") || isFalse && count == len("false") {
		lx.emit(itemBool)
		return lx.pop()
	}
	return lx.errorf("expected a boolean value")
}

// lexCommentStart begins the lexing of a comment. It will emit
// itemCommentStart and consume no characters, passing control to lexComment.
func lexCommentStart(lx *lexer) stateFn {
	lx.ignore()
	lx.emit(itemCommentStart)
	return lexComment
}

// lexComment lexes an entire comment. It assumes that '#' has been consumed.
// It will consume *up to* the first newline character, and pass control
// back to the last state on the stack.
func lexComment(lx *lexer) stateFn {
	switch r := lx.next(); {
	case isNL(r) || r == eof:
		lx.backup()
		lx.emit(itemText)
		return lx.pop()
	default:
		return lexComment
	}
}

// lexSkip ignores all slurped input and moves on to the next state.
func lexSkip(lx *lexer, nextState stateFn) stateFn {
	lx.ignore()
	return nextState
}

func isWhitespace(r rune) bool { return r == '\t' || r == ' ' }
func isNL(r rune) bool         { return r == '\n' || r == '\r' }
func isControl(r rune) bool { // Control characters except \t, \r, \n
	switch r {
	case '\t', '\r', '\n':
		return false
	default:
		return (r >= 0x00 && r <= 0x1f) || r == 0x7f
	}
}
func isDigit(r rune) bool  { return r >= '0' && r <= '9' }
func isBinary(r rune) bool { return r == '0' || r == '1' }
func isOctal(r rune) bool  { return r >= '0' && r <= '7' }
func isHex(r rune) bool    { return (r >= '0' && r <= '9') || (r|0x20 >= 'a' && r|0x20 <= 'f') }
func isBareKeyChar(r rune) bool {
	return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
		(r >= '0' && r <= '9') || r == '_' || r == '-'
}
