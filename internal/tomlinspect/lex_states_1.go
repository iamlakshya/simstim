// Derived from github.com/BurntSushi/toml v1.6.0 lex.go.
// Original MIT licence is in COPYING. Grammar functions are preserved.
package tomlinspect

import (
	"fmt"
	"unicode"
)

func lexTop(lx *lexer) stateFn {
	r := lx.next()
	if isWhitespace(r) || isNL(r) {
		return lexSkip(lx, lexTop)
	}
	switch r {
	case '#':
		lx.push(contLexTop)
		return lexCommentStart
	case '[':
		return lexTableStart
	case eof:
		if lx.pos > lx.start {
			// TODO: never reached? I think this can only occur on a bug in the
			// lexer(?)
			return lx.errorf("unexpected EOF")
		}
		lx.emit(itemEOF)
		return nil
	}

	// At this point, the only valid item can be a key, so we back up
	// and let the key lexer do the rest.
	lx.backup()
	lx.push(contLexTopEnd)
	return lexKeyStart
}

// lexTopEnd is entered whenever a top-level item has been consumed. (A value
// or a table.) It must see only whitespace, and will turn back to lexTop
// upon a newline. If it sees EOF, it will quit the lexer successfully.
func lexTopEnd(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case r == '#':
		// a comment will read to a newline for us.
		lx.push(contLexTop)
		return lexCommentStart
	case isWhitespace(r):
		return lexTopEnd
	case isNL(r):
		lx.ignore()
		return lexTop
	case r == eof:
		lx.emit(itemEOF)
		return nil
	}
	return lx.errorf("expected a top-level item to end with a newline, comment, or EOF, but got %q instead", r)
}

// lexTable lexes the beginning of a table. Namely, it makes sure that
// it starts with a character other than '.' and ']'.
// It assumes that '[' has already been consumed.
// It also handles the case that this is an item in an array of tables.
// e.g., '[[name]]'.
func lexTableStart(lx *lexer) stateFn {
	if lx.peek() == '[' {
		lx.next()
		lx.emit(itemArrayTableStart)
		lx.push(contLexArrayTableEnd)
	} else {
		lx.emit(itemTableStart)
		lx.push(contLexTableEnd)
	}
	return lexTableNameStart
}

func lexTableEnd(lx *lexer) stateFn {
	lx.emit(itemTableEnd)
	return lexTopEnd
}

func lexArrayTableEnd(lx *lexer) stateFn {
	if r := lx.next(); r != ']' {
		return lx.errorf("expected end of table array name delimiter ']', but got %q instead", r)
	}
	lx.emit(itemArrayTableEnd)
	return lexTopEnd
}

func lexTableNameStart(lx *lexer) stateFn {
	lx.skip(isWhitespace)
	switch r := lx.peek(); {
	case r == ']' || r == eof:
		return lx.errorf("unexpected end of table name (table names cannot be empty)")
	case r == '.':
		return lx.errorf("unexpected table separator (table names cannot be empty)")
	case r == '"' || r == '\'':
		lx.ignore()
		lx.push(contLexTableNameEnd)
		return lexQuotedName
	default:
		lx.push(contLexTableNameEnd)
		return lexBareName
	}
}

// lexTableNameEnd reads the end of a piece of a table name, optionally
// consuming whitespace.
func lexTableNameEnd(lx *lexer) stateFn {
	lx.skip(isWhitespace)
	switch r := lx.next(); {
	case r == '.':
		lx.ignore()
		return lexTableNameStart
	case r == ']':
		return lx.pop()
	default:
		return lx.errorf("expected '.' or ']' to end table name, but got %q instead", r)
	}
}

// lexBareName lexes one part of a key or table.
//
// It assumes that at least one valid character for the table has already been
// read.
//
// Lexes only one part, e.g. only 'a' inside 'a.b'.
func lexBareName(lx *lexer) stateFn {
	r := lx.next()
	if isBareKeyChar(r) {
		return lexBareName
	}
	lx.backup()
	lx.emit(itemText)
	return lx.pop()
}

// lexQuotedName lexes one part of a quoted key or table name. It assumes that
// it starts lexing at the quote itself (" or ').
//
// Lexes only one part, e.g. only '"a"' inside '"a".b'.
func lexQuotedName(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case r == '"':
		lx.ignore() // ignore the '"'
		return lexString
	case r == '\'':
		lx.ignore() // ignore the "'"
		return lexRawString

	// TODO: I don't think any of the below conditions can ever be reached?
	case isWhitespace(r):
		return lexSkip(lx, lexValue)
	case r == eof:
		return lx.errorf("unexpected EOF; expected value")
	default:
		return lx.errorf("expected value but found %q instead", r)
	}
}

// lexKeyStart consumes all key parts until a '='.
func lexKeyStart(lx *lexer) stateFn {
	lx.skip(isWhitespace)
	switch r := lx.peek(); {
	case r == '=' || r == eof:
		return lx.errorf("unexpected '=': key name appears blank")
	case r == '.':
		return lx.errorf("unexpected '.': keys cannot start with a '.'")
	case r == '"' || r == '\'':
		lx.ignore()
		fallthrough
	default: // Bare key
		lx.emit(itemKeyStart)
		return lexKeyNameStart
	}
}

func lexKeyNameStart(lx *lexer) stateFn {
	lx.skip(isWhitespace)
	switch r := lx.peek(); {
	default:
		lx.push(contLexKeyEnd)
		return lexBareName
	case r == '"' || r == '\'':
		lx.ignore()
		lx.push(contLexKeyEnd)
		return lexQuotedName

	// TODO: I think these can never be reached?
	case r == '=' || r == eof:
		return lx.errorf("unexpected '='")
	case r == '.':
		return lx.errorf("unexpected '.'")
	}
}

// lexKeyEnd consumes the end of a key and trims whitespace (up to the key
// separator).
func lexKeyEnd(lx *lexer) stateFn {
	lx.skip(isWhitespace)
	switch r := lx.next(); {
	case isWhitespace(r):
		return lexSkip(lx, lexKeyEnd)
	case r == eof: // TODO: never reached
		return lx.errorf("unexpected EOF; expected key separator '='")
	case r == '.':
		lx.ignore()
		return lexKeyNameStart
	case r == '=':
		lx.emit(itemKeyEnd)
		return lexSkip(lx, lexValue)
	default:
		if r == '\n' {
			return lx.errorPrevLine(fmt.Errorf("expected '.' or '=', but got %q instead", r))
		}
		return lx.errorf("expected '.' or '=', but got %q instead", r)
	}
}

// lexValue starts the consumption of a value anywhere a value is expected.
// lexValue will ignore whitespace.
// After a value is lexed, the last state on the next is popped and returned.
func lexValue(lx *lexer) stateFn {
	// We allow whitespace to precede a value, but NOT newlines.
	// In array syntax, the array states are responsible for ignoring newlines.
	r := lx.next()
	switch {
	case isWhitespace(r):
		return lexSkip(lx, lexValue)
	case isDigit(r):
		lx.backup() // avoid an extra state and use the same as above
		return lexNumberOrDateStart
	}
	switch r {
	case '[':
		lx.ignore()
		lx.emit(itemArray)
		return lexArrayValue
	case '{':
		lx.ignore()
		lx.emit(itemInlineTableStart)
		return lexInlineTableValue
	case '"':
		if lx.accept('"') {
			if lx.accept('"') {
				lx.ignore() // Ignore """
				return lexMultilineString
			}
			lx.backup()
		}
		lx.ignore() // ignore the '"'
		return lexString
	case '\'':
		if lx.accept('\'') {
			if lx.accept('\'') {
				lx.ignore() // Ignore """
				return lexMultilineRawString
			}
			lx.backup()
		}
		lx.ignore() // ignore the "'"
		return lexRawString
	case '.': // special error case, be kind to users
		return lx.errorf("floats must start with a digit, not '.'")
	case 'i', 'n':
		if (lx.accept('n') && lx.accept('f')) || (lx.accept('a') && lx.accept('n')) {
			lx.emit(itemFloat)
			return lx.pop()
		}
	case '-', '+':
		return lexDecimalNumberStart
	}
	if unicode.IsLetter(r) {
		// Be permissive here; lexBool will give a nice error if the
		// user wrote something like
		//   x = foo
		// (i.e. not 'true' or 'false' but is something else word-like.)
		lx.backup()
		return lexBool
	}
	if r == eof {
		return lx.errorf("unexpected EOF; expected value")
	}
	if r == '\n' {
		return lx.errorPrevLine(fmt.Errorf("expected value but found %q instead", r))
	}
	return lx.errorf("expected value but found %q instead", r)
}

// lexArrayValue consumes one value in an array. It assumes that '[' or ','
// have already been consumed. All whitespace and newlines are ignored.
func lexArrayValue(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case isWhitespace(r) || isNL(r):
		return lexSkip(lx, lexArrayValue)
	case r == '#':
		lx.push(contLexArrayValue)
		return lexCommentStart
	case r == ',':
		return lx.errorf("unexpected comma")
	case r == ']':
		return lexArrayEnd
	}

	lx.backup()
	lx.push(contLexArrayValueEnd)
	return lexValue
}

// lexArrayValueEnd consumes everything between the end of an array value and
// the next value (or the end of the array): it ignores whitespace and newlines
// and expects either a ',' or a ']'.
func lexArrayValueEnd(lx *lexer) stateFn {
	switch r := lx.next(); {
	case isWhitespace(r) || isNL(r):
		return lexSkip(lx, lexArrayValueEnd)
	case r == '#':
		lx.push(contLexArrayValueEnd)
		return lexCommentStart
	case r == ',':
		lx.ignore()
		return lexArrayValue // move on to the next value
	case r == ']':
		return lexArrayEnd
	default:
		return lx.errorf("expected a comma (',') or array terminator (']'), but got %s", runeOrEOF(r))
	}
}

// lexArrayEnd finishes the lexing of an array.
// It assumes that a ']' has just been consumed.
func lexArrayEnd(lx *lexer) stateFn {
	lx.ignore()
	lx.emit(itemArrayEnd)
	return lx.pop()
}

// lexInlineTableValue consumes one key/value pair in an inline table.
// It assumes that '{' or ',' have already been consumed. Whitespace is ignored.
func lexInlineTableValue(lx *lexer) stateFn {
	r := lx.next()
	switch {
	case isWhitespace(r):
		return lexSkip(lx, lexInlineTableValue)
	case isNL(r):
		return lexSkip(lx, lexInlineTableValue)
	case r == '#':
		lx.push(contLexInlineTableValue)
		return lexCommentStart
	case r == ',':
		return lx.errorf("unexpected comma")
	case r == '}':
		return lexInlineTableEnd
	}
	lx.backup()
	lx.push(contLexInlineTableValueEnd)
	return lexKeyStart
}

// lexInlineTableValueEnd consumes everything between the end of an inline table
// key/value pair and the next pair (or the end of the table):
// it ignores whitespace and expects either a ',' or a '}'.
func lexInlineTableValueEnd(lx *lexer) stateFn {
	switch r := lx.next(); {
	case isWhitespace(r):
		return lexSkip(lx, lexInlineTableValueEnd)
	case isNL(r):
		return lexSkip(lx, lexInlineTableValueEnd)
	case r == '#':
		lx.push(contLexInlineTableValueEnd)
		return lexCommentStart
	case r == ',':
		lx.ignore()
		lx.skip(isWhitespace)
		if lx.peek() == '}' {
			return lexInlineTableValueEnd
		}
		return lexInlineTableValue
	case r == '}':
		return lexInlineTableEnd
	default:
		return lx.errorf("expected a comma or an inline table terminator '}', but got %s instead", runeOrEOF(r))
	}
}

func runeOrEOF(r rune) string {
	if r == eof {
		return "end of file"
	}
	return "'" + string(r) + "'"
}

// lexInlineTableEnd finishes the lexing of an inline table.
// It assumes that a '}' has just been consumed.
func lexInlineTableEnd(lx *lexer) stateFn {
	lx.ignore()
	lx.emit(itemInlineTableEnd)
	return lx.pop()
}

// lexString consumes the inner contents of a string. It assumes that the
// beginning '"' has already been consumed and ignored.
