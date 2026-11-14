// Derived from github.com/BurntSushi/toml v1.6.0 lex.go.
// Original MIT licence is in COPYING. Grammar functions are preserved.
package tomlinspect

func lexString(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case r == eof:
		return lx.errorf(`unexpected EOF; expected '"'`)
	case isNL(r):
		return lx.errorPrevLine(errLexStringNL{})
	case r == '\\':
		lx.push(contLexString)
		return lexStringEscape
	case r == '"':
		lx.backup()
		if lx.esc {
			lx.esc = false
			lx.emit(itemStringEsc)
		} else {
			lx.emit(itemString)
		}
		lx.next()
		lx.ignore()
		return lx.pop()
	}
	return lexString
}

// lexMultilineString consumes the inner contents of a string. It assumes that
// the beginning '"""' has already been consumed and ignored.
func lexMultilineString(lx *lexer) stateFn {
	r := lx.next()
	switch r {
	default:
		return lexMultilineString
	case eof:
		return lx.errorf(`unexpected EOF; expected '"""'`)
	case '\\':
		return lexMultilineStringEscape
	case '"':
		/// Found " → try to read two more "".
		if lx.accept('"') {
			if lx.accept('"') {
				/// Peek ahead: the string can contain " and "", including at the
				/// end: """str"""""
				/// 6 or more at the end, however, is an error.
				if lx.peek() == '"' {
					/// Check if we already lexed 5 's; if so we have 6 now, and
					/// that's just too many man!
					///
					/// Second check is for the edge case:
					///
					///            two quotes allowed.
					///            vv
					///   """lol \""""""
					///          ^^  ^^^---- closing three
					///     escaped
					///
					/// But ugly, but it works
					if lx.currentSuffix(`"""""`) && !lx.currentSuffix(`\"""""`) {
						return lx.errorf(`unexpected '""""""'`)
					}
					lx.backup()
					lx.backup()
					return lexMultilineString
				}

				lx.backup() /// backup: don't include the """ in the item.
				lx.backup()
				lx.backup()
				lx.esc = false
				lx.emit(itemMultilineString)
				lx.next() /// Read over ''' again and discard it.
				lx.next()
				lx.next()
				lx.ignore()
				return lx.pop()
			}
			lx.backup()
		}
		return lexMultilineString
	}
}

// lexRawString consumes a raw string. Nothing can be escaped in such a string.
// It assumes that the beginning "'" has already been consumed and ignored.
func lexRawString(lx *lexer) stateFn {
	r := lx.next()
	switch {
	default:
		return lexRawString
	case r == eof:
		return lx.errorf(`unexpected EOF; expected "'"`)
	case isNL(r):
		return lx.errorPrevLine(errLexStringNL{})
	case r == '\'':
		lx.backup()
		lx.emit(itemRawString)
		lx.next()
		lx.ignore()
		return lx.pop()
	}
}

// lexMultilineRawString consumes a raw string. Nothing can be escaped in such a
// string. It assumes that the beginning triple-' has already been consumed and
// ignored.
func lexMultilineRawString(lx *lexer) stateFn {
	r := lx.next()
	switch r {
	default:
		return lexMultilineRawString
	case eof:
		return lx.errorf(`unexpected EOF; expected "'''"`)
	case '\'':
		/// Found ' → try to read two more ''.
		if lx.accept('\'') {
			if lx.accept('\'') {
				/// Peek ahead: the string can contain ' and '', including at the
				/// end: '''str'''''
				/// 6 or more at the end, however, is an error.
				if lx.peek() == '\'' {
					/// Check if we already lexed 5 's; if so we have 6 now, and
					/// that's just too many man!
					if lx.currentSuffix("'''''") {
						return lx.errorf(`unexpected "''''''"`)
					}
					lx.backup()
					lx.backup()
					return lexMultilineRawString
				}

				lx.backup() /// backup: don't include the ''' in the item.
				lx.backup()
				lx.backup()
				lx.emit(itemRawMultilineString)
				lx.next() /// Read over ''' again and discard it.
				lx.next()
				lx.next()
				lx.ignore()
				return lx.pop()
			}
			lx.backup()
		}
		return lexMultilineRawString
	}
}

// lexMultilineStringEscape consumes an escaped character. It assumes that the
// preceding '\\' has already been consumed.
func lexMultilineStringEscape(lx *lexer) stateFn {
	if isNL(lx.next()) { /// \ escaping newline.
		return lexMultilineString
	}
	lx.backup()
	lx.push(contLexMultilineString)
	return lexStringEscape(lx)
}

func lexStringEscape(lx *lexer) stateFn {
	lx.esc = true
	r := lx.next()
	switch r {
	case 'e':
		fallthrough
	case 'b':
		fallthrough
	case 't':
		fallthrough
	case 'n':
		fallthrough
	case 'f':
		fallthrough
	case 'r':
		fallthrough
	case '"':
		fallthrough
	case ' ', '\t':
		// Inside """ .. """ strings you can use \ to escape newlines, and any
		// amount of whitespace can be between the \ and \n.
		fallthrough
	case '\\':
		return lx.pop()
	case 'x':
		return lexHexEscape
	case 'u':
		return lexShortUnicodeEscape
	case 'U':
		return lexLongUnicodeEscape
	}
	return lx.error(errLexEscape{r})
}

func lexHexEscape(lx *lexer) stateFn {
	var r rune
	for i := 0; i < 2; i++ {
		r = lx.next()
		if !isHex(r) {
			return lx.errorf(`expected two hexadecimal digits after '\x', but got %q instead`, redactedSpanDiagnostic(lx.current()))
		}
	}
	return lx.pop()
}

func lexShortUnicodeEscape(lx *lexer) stateFn {
	var r rune
	for i := 0; i < 4; i++ {
		r = lx.next()
		if !isHex(r) {
			return lx.errorf(`expected four hexadecimal digits after '\u', but got %q instead`, redactedSpanDiagnostic(lx.current()))
		}
	}
	return lx.pop()
}

func lexLongUnicodeEscape(lx *lexer) stateFn {
	var r rune
	for i := 0; i < 8; i++ {
		r = lx.next()
		if !isHex(r) {
			return lx.errorf(`expected eight hexadecimal digits after '\U', but got %q instead`, redactedSpanDiagnostic(lx.current()))
		}
	}
	return lx.pop()
}

// lexNumberOrDateStart processes the first character of a value which begins
// with a digit. It exists to catch values starting with '0', so that
// lexBaseNumberOrDate can differentiate base prefixed integers from other
// types.
func lexNumberOrDateStart(lx *lexer) stateFn {
	if lx.next() == '0' {
		return lexBaseNumberOrDate
	}
	return lexNumberOrDate
}

// lexNumberOrDate consumes either an integer, float or datetime.
func lexNumberOrDate(lx *lexer) stateFn {
	r := lx.next()
	if isDigit(r) {
		return lexNumberOrDate
	}
	switch r {
	case '-', ':':
		return lexDatetime
	case '_':
		return lexDecimalNumber
	case '.', 'e', 'E':
		return lexFloat
	}

	lx.backup()
	lx.emit(itemInteger)
	return lx.pop()
}

// lexDatetime consumes a Datetime, to a first approximation.
// The parser validates that it matches one of the accepted formats.
func lexDatetime(lx *lexer) stateFn {
	r := lx.next()
	if isDigit(r) {
		return lexDatetime
	}
	switch r {
	case '-', ':', 'T', 't', ' ', '.', 'Z', 'z', '+':
		return lexDatetime
	}

	lx.backup()
	lx.emitTrim(itemDatetime)
	return lx.pop()
}

// lexHexInteger consumes a hexadecimal integer after seeing the '0x' prefix.
func lexHexInteger(lx *lexer) stateFn {
	r := lx.next()
	if isHex(r) {
		return lexHexInteger
	}
	switch r {
	case '_':
		return lexHexInteger
	}

	lx.backup()
	lx.emit(itemInteger)
	return lx.pop()
}

// lexOctalInteger consumes an octal integer after seeing the '0o' prefix.
func lexOctalInteger(lx *lexer) stateFn {
	r := lx.next()
	if isOctal(r) {
		return lexOctalInteger
	}
	switch r {
	case '_':
		return lexOctalInteger
	}

	lx.backup()
	lx.emit(itemInteger)
	return lx.pop()
}

// lexBinaryInteger consumes a binary integer after seeing the '0b' prefix.
func lexBinaryInteger(lx *lexer) stateFn {
	r := lx.next()
	if isBinary(r) {
		return lexBinaryInteger
	}
	switch r {
	case '_':
		return lexBinaryInteger
	}

	lx.backup()
	lx.emit(itemInteger)
	return lx.pop()
}

// lexDecimalNumber consumes a decimal float or integer.
func lexDecimalNumber(lx *lexer) stateFn {
	r := lx.next()
	if isDigit(r) {
		return lexDecimalNumber
	}
	switch r {
	case '.', 'e', 'E':
		return lexFloat
	case '_':
		return lexDecimalNumber
	}

	lx.backup()
	lx.emit(itemInteger)
	return lx.pop()
}

// lexDecimalNumber consumes the first digit of a number beginning with a sign.
// It assumes the sign has already been consumed. Values which start with a sign
// are only allowed to be decimal integers or floats.
//
// The special "nan" and "inf" values are also recognized.
func lexDecimalNumberStart(lx *lexer) stateFn {
	r := lx.next()

	// Special error cases to give users better error messages
	switch r {
	case 'i':
		if !lx.accept('n') || !lx.accept('f') {
			return lx.errorf("invalid float: '%s'", redactedSpanDiagnostic(lx.current()))
		}
		lx.emit(itemFloat)
		return lx.pop()
	case 'n':
		if !lx.accept('a') || !lx.accept('n') {
			return lx.errorf("invalid float: '%s'", redactedSpanDiagnostic(lx.current()))
		}
		lx.emit(itemFloat)
		return lx.pop()
	case '0':
		p := lx.peek()
		switch p {
		case 'b', 'o', 'x':
			return lx.errorf("cannot use sign with non-decimal numbers: '%s%c'", redactedSpanDiagnostic(lx.current()), p)
		}
	case '.':
		return lx.errorf("floats must start with a digit, not '.'")
	}

	if isDigit(r) {
		return lexDecimalNumber
	}

	return lx.errorf("expected a digit but got %q", r)
}

// lexBaseNumberOrDate differentiates between the possible values which
// start with '0'. It assumes that before reaching this state, the initial '0'
// has been consumed.
