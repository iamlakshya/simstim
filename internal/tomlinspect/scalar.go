package tomlinspect

import "time"

// ScalarKind is the pinned native scalar type, before schema conversion.
type ScalarKind byte

const (
	// StringScalar contains borrowed decoded text.
	StringScalar ScalarKind = iota
	// BoolScalar contains a native bool.
	BoolScalar
	// IntegerScalar contains a native int64.
	IntegerScalar
	// FloatScalar contains a native float64, including signed zero and NaN.
	FloatScalar
	// DatetimeScalar contains a native time.Time and its pinned zone.
	DatetimeScalar
)

// ScalarCursor owns no decoded string or token buffer. A string remains a
// borrowed Text cursor; every other scalar has the pin's bounded native value.
// It is valid only during the Inspect call that owns the pinned source.
type ScalarCursor struct {
	kind     ScalarKind
	text     Text
	boolean  bool
	integer  int64
	floating float64
	datetime time.Time
}

// Kind returns the source's native TOML scalar type.
func (s ScalarCursor) Kind() ScalarKind { return s.kind }

// Text returns the borrowed string text and whether this is a TOML string.
func (s ScalarCursor) Text() (Text, bool) { return s.text, s.kind == StringScalar }

// Bool returns the value and whether this is a TOML bool.
func (s ScalarCursor) Bool() (bool, bool) { return s.boolean, s.kind == BoolScalar }

// Integer returns the value and whether this is a TOML integer.
func (s ScalarCursor) Integer() (int64, bool) { return s.integer, s.kind == IntegerScalar }

// Float returns the value and whether this is a TOML float.
func (s ScalarCursor) Float() (float64, bool) { return s.floating, s.kind == FloatScalar }

// Datetime returns the value and whether this is a TOML datetime, date or time.
func (s ScalarCursor) Datetime() (time.Time, bool) { return s.datetime, s.kind == DatetimeScalar }

func nativeScalar(input *source, it item) (ScalarCursor, error) {
	view := numberSpan{source: input, span: it.val}
	var scalar ScalarCursor
	var err error
	switch it.typ {
	case itemString, itemStringEsc, itemRawString, itemMultilineString, itemRawMultilineString:
		scalar.kind = StringScalar
		scalar.text = Text{source: input, span: it.val, kind: it.typ, generation: input.generation}
		err = scalar.text.Walk(func(rune) error { return nil })
	case itemBool:
		scalar.kind = BoolScalar
		if view.equal("true") {
			scalar.boolean = true
		} else if !view.equal("false") {
			err = ErrSyntax
		}
	case itemInteger:
		scalar.kind = IntegerScalar
		scalar.integer, err = integerValue(view)
	case itemFloat:
		scalar.kind = FloatScalar
		scalar.floating, err = floatValue(view)
	case itemDatetime:
		scalar.kind = DatetimeScalar
		scalar.datetime, err = dateValue(view)
	default:
		err = ErrSyntax
	}
	return scalar, err
}
