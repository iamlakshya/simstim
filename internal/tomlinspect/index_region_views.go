package tomlinspect

// pinTOMLPage preserves the permanently live owner-zero domain. P23 region
// views can never adopt its roots, even if a class/record number is identical.
func pinTOMLPage(pages *indexPages, id uint32) ([]byte, int, error) {
	data, slot, err := pages.pin(id)
	if err != nil {
		return nil, 0, err
	}
	if read16(data, 10) != 0 {
		pages.release(slot, false)
		return nil, 0, ErrIndexCorrupt
	}
	return data, slot, nil
}

func (d *recordDirectory) pin(id uint32) ([]byte, int, error) {
	if d.region == nil {
		return pinTOMLPage(d.indexPages, id)
	}
	return d.region.pin(d.ref, id)
}
func (d *recordDirectory) allocate(kind, class byte) (uint32, []byte, int, error) {
	if d.region == nil {
		return d.indexPages.allocate(kind, class)
	}
	return d.region.allocate(d.ref, kind, class)
}
func (s *numericTrees) pin(id uint32) ([]byte, int, error) {
	if s.region == nil {
		return pinTOMLPage(s.indexPages, id)
	}
	return s.region.pin(s.ref, id)
}
func (s *numericTrees) allocate(kind, class byte) (uint32, []byte, int, error) {
	if s.region == nil {
		return s.indexPages.allocate(kind, class)
	}
	return s.region.allocate(s.ref, kind, class)
}

// These constructors are private. The outer region book owns the fixed root
// catalogues and validates layout IDs before forming any public borrower.
func (r *regionPages) directoryView(ref regionRef, class byte, root *uint32) (recordDirectory, error) {
	if err := r.check(ref); err != nil {
		return recordDirectory{}, err
	}
	if root == nil {
		return recordDirectory{}, r.fail(ErrIndexInvariant)
	}
	return recordDirectory{indexPages: r.pages, root: root, class: class, region: r, ref: ref}, nil
}
func (r *regionPages) treeView(ref regionRef, roots *[treeCount]uint32) (numericTrees, error) {
	if err := r.check(ref); err != nil {
		return numericTrees{}, err
	}
	if roots == nil {
		return numericTrees{}, r.fail(ErrIndexInvariant)
	}
	return numericTrees{indexPages: r.pages, roots: roots, region: r, ref: ref}, nil
}
