package tomlinspect

// Walk visits the complete immutable identity relation in comparison order.
// Unlike WalkFirst, it makes no numeric grouping or lower-bound assumption.
// The callback must preserve this relation's keys and values until it returns.
func (t Tree) Walk(visit func(uint64, uint64, uint64) error) error {
	i, book, err := t.checked()
	if err != nil {
		return err
	}
	if t.slot != 1 || t.kind != RelationIdentity || visit == nil || i.outer.object.active || i.outer.object.pending.id != 0 {
		return i.scopeFailure(ErrIndexInvariant)
	}
	if err := i.outer.regions.enter(book.ref); err != nil {
		return i.scopeFailure(err)
	}
	defer i.outer.regions.leave(book.ref)
	view, err := t.view(i, book)
	if err != nil {
		return err
	}
	var seen uint64
	rows := book.rows[t.which]
	err = view.walkOrdered(t.which, func(key numericKey, raw numericValue) error {
		if _, err := t.key(i, uint64(key.first), uint64(key.second), false); err != nil {
			return err
		}
		value := read64(raw[:], 0)
		if value == 0 || value > uint64(book.classes[layoutEntry].count) || seen >= rows {
			return ErrIndexCorrupt
		}
		seen++
		return visit(uint64(key.first), uint64(key.second), value)
	})
	if err != nil {
		return i.scopeFailure(err)
	}
	if seen != rows || book.rows[t.which] != rows {
		return i.scopeFailure(ErrIndexCorrupt)
	}
	return nil
}
