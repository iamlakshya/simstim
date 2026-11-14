package tomlinspect

// ValueType is a final native shape, after both pinned object planes settle.
type ValueType byte

const (
	// ScalarValue contains a native scalar source reference.
	ScalarValue ValueType = iota + 1
	// TableValue contains a final map object.
	TableValue
	// ArrayValue contains an ordinary array or array of tables.
	ArrayValue
)

// ValueCursor is a borrowed final object/source reference, not an owned
// Settings value or a permission. It is invalid after Inspect returns.
type ValueCursor struct {
	inspector *Inspector
	epoch     uint64
	path      PathCursor
	value     storedValue
}

// Type returns the final value shape.
func (v ValueCursor) Type() ValueType {
	switch v.value.kind {
	case storedMap:
		return TableValue
	case storedArray, storedTableArray:
		return ArrayValue
	default:
		return ScalarValue
	}
}

// Path returns its exact borrowed logical/object path.
func (v ValueCursor) Path() PathCursor { return v.path }

// Scalar returns the pinned native scalar without retaining decoded strings.
func (v ValueCursor) Scalar() (scalar ScalarCursor, err error) {
	defer cursorError(&err)
	if err := v.inspector.borrowed(v.epoch); err != nil {
		return scalar, err
	}
	if v.value.kind != storedScalar {
		return scalar, ErrSyntax
	}
	if v.value.token == itemString || v.value.token == itemStringEsc || v.value.token == itemRawString || v.value.token == itemMultilineString || v.value.token == itemRawMultilineString {
		// The complete native parser validated this SAME pinned token.
		// Borrow its decoded cursor; do not rescan an unused command merely
		// to ask whether its final value is a string.
		return ScalarCursor{kind: StringScalar, text: Text{source: &v.inspector.source, span: v.value.span, kind: v.value.token, generation: v.epoch}}, nil
	}
	return nativeScalar(&v.inspector.source, item{typ: v.value.token, val: v.value.span})
}

// ScalarType returns the already validated native type without converting
// or retaining a source value a second time.
func (v ValueCursor) ScalarType() (ScalarKind, error) {
	if err := v.inspector.borrowed(v.epoch); err != nil {
		return 0, err
	}
	if v.value.kind != storedScalar {
		return 0, ErrSyntax
	}
	switch v.value.token {
	case itemString, itemStringEsc, itemRawString, itemMultilineString, itemRawMultilineString:
		return StringScalar, nil
	case itemBool:
		return BoolScalar, nil
	case itemInteger:
		return IntegerScalar, nil
	case itemFloat:
		return FloatScalar, nil
	case itemDatetime:
		return DatetimeScalar, nil
	default:
		return 0, ErrIndexCorrupt
	}
}

// Table returns a borrowed final table cursor.
func (v ValueCursor) Table() (TableCursor, error) {
	if err := v.inspector.borrowed(v.epoch); err != nil {
		return TableCursor{}, err
	}
	if v.value.kind != storedMap {
		return TableCursor{}, ErrSyntax
	}
	return TableCursor{value: v}, nil
}

// Array returns a borrowed final array cursor, including every table-array row.
func (v ValueCursor) Array() (ArrayCursor, error) {
	if err := v.inspector.borrowed(v.epoch); err != nil {
		return ArrayCursor{}, err
	}
	if v.value.kind != storedArray && v.value.kind != storedTableArray {
		return ArrayCursor{}, ErrSyntax
	}
	return ArrayCursor{value: v}, nil
}

// Visit dispatches one final value. The visitor walks known children through
// the supplied cursors; unknown input already passed the complete parser.
func (v ValueCursor) Visit(visitor SchemaVisitor) error {
	if err := v.inspector.borrowed(v.epoch); err != nil {
		return err
	}
	if visitor == nil {
		return ErrIndexInvariant
	}
	switch v.value.kind {
	case storedMap:
		if err := visitor.EnterTable(v.path, TableCursor{value: v}); err != nil {
			return err
		}
		return visitor.LeaveTable(v.path)
	case storedArray, storedTableArray:
		if err := visitor.EnterArray(v.path, ArrayCursor{value: v}); err != nil {
			return err
		}
		return visitor.LeaveArray(v.path)
	case storedScalar:
		scalar, err := v.Scalar()
		if err != nil {
			return err
		}
		return visitor.Scalar(v.path, scalar)
	default:
		return ErrIndexCorrupt
	}
}

// TableCursor streams the actual final map, including returned-map winners.
type TableCursor struct{ value ValueCursor }

// Walk visits every direct key once; no decoded name set is retained.
func (t TableCursor) Walk(visit func(PathCursor, ValueCursor) error) (err error) {
	defer cursorError(&err)
	v := t.value
	if err := v.inspector.borrowed(v.epoch); err != nil {
		return err
	}
	if v.value.kind != storedMap || visit == nil {
		return ErrSyntax
	}
	return v.inspector.store.mapEntries(v.value.object, func(name uint32, stored storedValue) error {
		id, err := v.inspector.store.lookupPath(v.path.id, name)
		if err != nil {
			return err
		}
		path := PathCursor{inspector: v.inspector, epoch: v.epoch, id: id, scope: v.value.object, array: v.path.array, index: v.path.index}
		child := ValueCursor{inspector: v.inspector, epoch: v.epoch, path: path, value: stored}
		return visit(path, child)
	})
}

// ArrayCursor streams final ordinary elements or every final table-array row.
type ArrayCursor struct{ value ValueCursor }

// Walk visits elements in their exact original order.
func (a ArrayCursor) Walk(visit func(uint32, PathCursor, ValueCursor) error) (err error) {
	defer cursorError(&err)
	v := a.value
	if err := v.inspector.borrowed(v.epoch); err != nil {
		return err
	}
	if visit == nil {
		return ErrSyntax
	}
	return v.inspector.store.arrayValues(v.value.object, func(index uint32, stored storedValue) error {
		path := v.path
		path.scope, path.array, path.index = v.value.object, v.value.object, index
		child := ValueCursor{inspector: v.inspector, epoch: v.epoch, path: path, value: stored}
		return visit(index, path, child)
	})
}

// Len returns the final array length without materialising its elements.
func (a ArrayCursor) Len() (uint32, error) {
	v := a.value
	if err := v.inspector.borrowed(v.epoch); err != nil {
		return 0, err
	}
	info, err := v.inspector.store.object(v.value.object)
	if err != nil {
		return 0, err
	}
	if info.kind != storedArray && info.kind != storedTableArray {
		return 0, ErrSyntax
	}
	return info.count, nil
}

// Value returns the same borrowed final value, without copying its content.
func (t TableCursor) Value() ValueCursor { return t.value }

// Value returns the same borrowed final array value.
func (a ArrayCursor) Value() ValueCursor { return a.value }
