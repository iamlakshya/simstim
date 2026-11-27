package tomlinspect

// Floor finds the greatest current run START<=first. Only the numeric
// EntryRegion.RunOffsets tree exposes this operation; source relations refuse.
// The semantic caller still verifies offset<start+count*width with checked
// actual slot facts. This lookup creates no per-character index or cap.
func (t Tree) Floor(first uint64) (matchedFirst, value uint64, found bool, err error) {
	i, book, err := t.checked()
	if err != nil {
		return 0, 0, false, err
	}
	if t.slot != 2 || t.entry == nil || t.kind != 0 {
		return 0, 0, false, i.scopeFailure(ErrIndexInvariant)
	}
	key, err := t.key(i, first, 0, false)
	if err != nil {
		return 0, 0, false, err
	}
	if err = i.outer.regions.enter(book.ref); err != nil {
		return 0, 0, false, i.scopeFailure(err)
	}
	defer i.outer.regions.leave(book.ref)
	view, err := t.view(i, book)
	if err != nil {
		return 0, 0, false, err
	}
	matched, raw, found, err := view.floor(t.which, key)
	if err != nil {
		return 0, 0, false, i.scopeFailure(err)
	}
	if !found {
		return 0, 0, false, nil
	}
	value = read64(raw[:], 0)
	if matched.second != 0 || matched.first > key.first || value == 0 || value > uint64(i.outer.books[1].classes[layoutSlot].count) {
		return 0, 0, false, i.scopeFailure(ErrIndexCorrupt)
	}
	return uint64(matched.first), value, true, nil
}
