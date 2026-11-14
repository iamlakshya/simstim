// Derived from BurntSushi/toml v1.6.0. See COPYING.
package tomlinspect

type itemType int

const (
	itemError itemType = iota
	itemEOF
	itemText
	itemString
	itemStringEsc
	itemRawString
	itemMultilineString
	itemRawMultilineString
	itemBool
	itemInteger
	itemFloat
	itemDatetime
	itemArray // the start of an array
	itemArrayEnd
	itemTableStart
	itemTableEnd
	itemArrayTableStart
	itemArrayTableEnd
	itemKeyStart
	itemKeyEnd
	itemCommentStart
	itemInlineTableStart
	itemInlineTableEnd
)

const eof = 0

type stateFn func(lx *lexer) stateFn
