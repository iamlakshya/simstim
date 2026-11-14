package tomlinspect

// RelationKind selects one of the THREE global state trees. The fourth
// P23 tree is the current numeric run-offset tree in its own phase.
type RelationKind uint8

const (
	RelationSentinel RelationKind = iota + 1
	RelationIdentity
	RelationHosts
)

type Tree struct {
	state *StateRegion
	entry *EntryRegion
	slot  uint16
	which byte
	kind  RelationKind
}
type regionComparison struct {
	h           *Inspection
	stateID     uint64
	kind        RelationKind
	compareKeys func(uint64, uint64, uint64, uint64) (int, error)
}

func (c *regionComparison) compare(a, b numericKey) (int, error) {
	i, err := c.h.check()
	if err != nil {
		return 0, err
	}
	if !i.outer.stateActive || i.outer.stateID != c.stateID || i.outer.comparing || c.compareKeys == nil {
		return 0, i.scopeFailure(ErrIndexInvariant)
	}
	// Host grouping and its lower-bound sentinel are physical invariants,
	// not a promise delegated to a caller comparison callback.
	if c.kind == RelationHosts {
		if a.first != b.first {
			if a.first < b.first {
				return -1, nil
			}
			return 1, nil
		}
		if a.second == 0 || b.second == 0 {
			if a.second == b.second {
				return 0, nil
			}
			if a.second == 0 {
				return -1, nil
			}
			return 1, nil
		}
	}
	i.outer.comparing = true
	defer func() { i.outer.comparing = false }()
	order, err := c.compareKeys(uint64(a.first), uint64(a.second), uint64(b.first), uint64(b.second))
	if err != nil {
		return 0, i.scopeFailure(err)
	}
	if i.outer.failure != nil {
		return 0, i.outer.failure
	}
	return order, nil
}
func (s *StateRegion) NewTree(kind RelationKind, compare func(uint64, uint64, uint64, uint64) (int, error)) (Tree, error) {
	i, err := s.check()
	if err != nil {
		return Tree{}, err
	}
	if err = i.metadataWriteAllowed(); err != nil {
		return Tree{}, err
	}
	if kind < RelationSentinel || kind > RelationHosts || compare == nil {
		return Tree{}, i.scopeFailure(ErrIndexInvariant)
	}
	which := byte(kind - 1)
	book := &i.outer.books[1]
	if book.trees[which] {
		return Tree{}, i.scopeFailure(ErrIndexInvariant)
	}
	book.trees[which] = true
	book.comparisons[which] = regionComparison{h: s.h, stateID: s.id, kind: kind, compareKeys: compare}
	return Tree{state: s, slot: 1, which: which, kind: kind}, nil
}
func (t Tree) checked() (*Inspector, *regionBook, error) {
	var i *Inspector
	var err error
	if t.entry != nil {
		i, err = t.entry.check()
	} else if t.state != nil {
		i, err = t.state.check()
	} else {
		return nil, nil, ErrIndexInvariant
	}
	if err != nil {
		return nil, nil, err
	}
	if i.outer.comparing {
		return nil, nil, i.scopeFailure(ErrIndexInvariant)
	}
	book := &i.outer.books[t.slot]
	if int(t.which) >= len(book.roots) || !book.trees[t.which] {
		return nil, nil, i.scopeFailure(ErrIndexInvariant)
	}
	return i, book, nil
}
func (t Tree) view(i *Inspector, book *regionBook) (numericTrees, error) {
	view, err := i.outer.regions.treeView(book.ref, &book.roots)
	if err != nil {
		return view, i.scopeFailure(err)
	}
	if t.slot == 1 {
		view.comparison = &book.comparisons[t.which]
		view.prefixGrouped = t.kind == RelationHosts
	}
	return view, nil
}
func (t Tree) key(i *Inspector, first, second uint64, insert bool) (numericKey, error) {
	if first > uint64(^uint32(0)) || second > uint64(^uint32(0)) {
		return numericKey{}, i.scopeFailure(ErrIndexInvariant)
	}
	if t.slot == 2 {
		if second != 0 {
			return numericKey{}, i.scopeFailure(ErrIndexInvariant)
		}
		return numericKey{uint32(first), 0}, nil
	}
	if t.kind == RelationHosts {
		if first == 0 || first > uint64(i.outer.books[1].classes[layoutBinding].count) || second == 0 || second > uint64(i.outer.books[1].classes[layoutHost].count) {
			return numericKey{}, i.scopeFailure(ErrIndexCorrupt)
		}
		if insert {
			host, err := t.state.Host(second)
			if err != nil {
				return numericKey{}, err
			}
			if host.BindingID != first {
				return numericKey{}, i.scopeFailure(ErrIndexCorrupt)
			}
		}
	} else if second != 0 || first == 0 || first > uint64(i.outer.books[1].classes[layoutEntry].count) {
		return numericKey{}, i.scopeFailure(ErrIndexCorrupt)
	}
	return numericKey{uint32(first), uint32(second)}, nil
}
func (t Tree) Find(first, second uint64) (uint64, bool, error) {
	i, book, err := t.checked()
	if err != nil {
		return 0, false, err
	}
	key, err := t.key(i, first, second, false)
	if err != nil {
		return 0, false, err
	}
	if err = i.outer.regions.enter(book.ref); err != nil {
		return 0, false, i.scopeFailure(err)
	}
	defer i.outer.regions.leave(book.ref)
	view, err := t.view(i, book)
	if err != nil {
		return 0, false, err
	}
	raw, found, err := view.get(t.which, key)
	if err != nil {
		return 0, false, i.scopeFailure(err)
	}
	return read64(raw[:], 0), found, nil
}
func (t Tree) Put(first, second, value uint64) (bool, error) {
	i, book, err := t.checked()
	if err != nil {
		return false, err
	}
	if err = i.metadataWriteAllowed(); err != nil {
		return false, err
	}
	key, err := t.key(i, first, second, true)
	if err != nil {
		return false, err
	}
	if value == 0 {
		return false, i.scopeFailure(ErrIndexCorrupt)
	}
	if t.slot == 2 {
		if value > uint64(i.outer.books[1].classes[layoutSlot].count) {
			return false, i.scopeFailure(ErrIndexCorrupt)
		}
	} else if t.kind == RelationHosts {
		if value > uint64(i.outer.books[1].classes[layoutHost].count) {
			return false, i.scopeFailure(ErrIndexCorrupt)
		}
	} else if value > uint64(i.outer.books[1].classes[layoutEntry].count) {
		return false, i.scopeFailure(ErrIndexCorrupt)
	}
	if err = i.outer.regions.enter(book.ref); err != nil {
		return false, i.scopeFailure(err)
	}
	defer i.outer.regions.leave(book.ref)
	view, err := t.view(i, book)
	if err != nil {
		return false, err
	}
	_, found, err := view.get(t.which, key)
	if err != nil {
		return false, i.scopeFailure(err)
	}
	limit := i.outer.stateCounts.entries
	if t.kind == RelationHosts {
		limit = i.outer.stateCounts.hosts
	}
	if t.slot == 2 {
		limit = i.outer.entryCounts.runRows
	}
	if !found && book.rows[t.which] >= limit {
		return false, i.scopeFailure(ErrIndexInvariant)
	}
	var raw numericValue
	write64(raw[:], 0, value)
	if err = view.put(t.which, key, raw); err != nil {
		return false, i.scopeFailure(err)
	}
	if !found {
		book.rows[t.which]++
	}
	return !found, nil
}
func (t Tree) WalkFirst(first uint64, visit func(uint64, uint64, uint64) error) error {
	i, book, err := t.checked()
	if err != nil {
		return err
	}
	if t.kind != RelationHosts || t.slot != 1 || first == 0 || first > uint64(i.outer.books[1].classes[layoutBinding].count) || visit == nil {
		return i.scopeFailure(ErrIndexInvariant)
	}
	if err = i.outer.regions.enter(book.ref); err != nil {
		return i.scopeFailure(err)
	}
	defer i.outer.regions.leave(book.ref)
	view, err := t.view(i, book)
	if err != nil {
		return err
	}
	err = view.walk(t.which, uint32(first), func(key numericKey, raw numericValue) error {
		return visit(uint64(key.first), uint64(key.second), read64(raw[:], 0))
	})
	if err != nil {
		return i.scopeFailure(err)
	}
	return nil
}
