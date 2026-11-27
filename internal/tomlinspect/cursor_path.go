package tomlinspect

import "context"

// PathCursor retains exact logical and selected-object/array identity using
// metadata IDs and source spans. It contains no decoded key or path string.
type PathCursor struct {
	inspector               *Inspector
	epoch                   uint64
	id, scope, array, index uint32
}

// Depth returns the logical component count, without materialising a path.
func (p PathCursor) Depth() (int, error) {
	if err := p.inspector.borrowed(p.epoch); err != nil {
		return 0, err
	}
	info, err := p.inspector.store.path(p.id)
	return int(info.depth), err
}

// Name returns the final component's borrowed decoded text.
func (p PathCursor) Name() (Text, error) {
	if err := p.inspector.borrowed(p.epoch); err != nil {
		return Text{}, err
	}
	if p.id == 0 {
		return Text{}, ErrSyntax
	}
	info, err := p.inspector.store.path(p.id)
	if err != nil {
		return Text{}, err
	}
	name, err := p.inspector.store.readName(info.name)
	if err != nil {
		return Text{}, err
	}
	if name.kind != nameLeaf {
		return Text{}, ErrIndexCorrupt
	}
	return name.text, nil
}

// Term resolves a component by exact parent IDs. It does not copy decoded
// ancestors. Closed schema visitors use Name and fixed schema depth.
func (p PathCursor) Term(index int) (Text, error) {
	if err := p.inspector.borrowed(p.epoch); err != nil {
		return Text{}, err
	}
	id := p.id
	info, err := p.inspector.store.path(id)
	if err != nil {
		return Text{}, err
	}
	if index < 0 || uint32(index) >= info.depth {
		return Text{}, ErrSyntax
	}
	for info.depth > uint32(index)+1 {
		id = info.parent
		info, err = p.inspector.store.path(id)
		if err != nil {
			return Text{}, err
		}
	}
	name, err := p.inspector.store.readName(info.name)
	if err != nil {
		return Text{}, err
	}
	if name.kind != nameLeaf {
		return Text{}, ErrIndexCorrupt
	}
	return name.text, nil
}

// Scope returns the exact selected parent object ID for this inspection.
func (p PathCursor) Scope() uint32 { return p.scope }

// Element returns the exact array object and element index when applicable.
func (p PathCursor) Element() (array, index uint32, present bool) {
	return p.array, p.index, p.array != 0
}

// Context is the inspection's cancellation context, valid only while borrowed.
func (p PathCursor) Context() (context.Context, error) {
	if err := p.inspector.borrowed(p.epoch); err != nil {
		return nil, err
	}
	return p.inspector.source.ctx, nil
}
