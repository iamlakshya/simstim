package tomlinspect

func (p *metadataParser) assignment(start item) error {
	sequence, err := p.keys(itemKeyEnd)
	if err != nil {
		return err
	}
	base := p.frames.current.scope
	ctx := base
	for index := uint32(0); index < sequence.count-1; index++ {
		name, err := p.store.keyAt(sequence, index)
		if err != nil {
			return err
		}
		path, err := p.store.internPath(ctx.path, name)
		if err != nil {
			return err
		}
		ctx, err = p.store.implicitContext(ctx, name, path)
		if err != nil {
			return &Error{Position: p.position, Cause: err}
		}
	}
	name, err := p.store.keyAt(sequence, sequence.count-1)
	if err != nil {
		return err
	}
	path, err := p.store.internPath(ctx.path, name)
	if err != nil {
		return err
	}
	p.current = assignment{base: base, context: ctx, name: name, path: path, keys: sequence, keyPosition: start.pos}
	return nil
}

func (p *metadataParser) header(start item) error {
	end := itemTableEnd
	if start.typ == itemArrayTableStart {
		end = itemArrayTableEnd
	}
	sequence, err := p.keys(end)
	if err != nil {
		return err
	}
	ctx := parseContext{object: p.root.object}
	for index := uint32(0); index < sequence.count-1; index++ {
		name, err := p.store.keyAt(sequence, index)
		if err != nil {
			return err
		}
		path, err := p.store.internPath(ctx.path, name)
		if err != nil {
			return err
		}
		ctx, err = p.store.headerParent(ctx, name, path)
		if err != nil {
			return &Error{Position: p.position, Cause: err}
		}
	}
	name, err := p.store.keyAt(sequence, sequence.count-1)
	if err != nil {
		return err
	}
	path, err := p.store.internPath(ctx.path, name)
	if err != nil {
		return err
	}
	kind := byte(pathTable)
	if start.typ == itemArrayTableStart {
		ctx, err = p.store.appendTable(ctx, name, path)
		kind = pathTableArray
	} else {
		fresh, failure := p.store.newObject(storedMap, 0)
		if failure != nil {
			return failure
		}
		actual, failure := p.store.globalValue(ctx, name, path, fresh)
		if failure != nil {
			return &Error{Position: p.position, Cause: failure}
		}
		ctx, err = p.store.contextValue(path, actual)
	}
	if err != nil {
		return &Error{Position: p.position, Cause: err}
	}
	if err := p.store.typeAt(path, kind, start.pos); err != nil {
		return err
	}
	p.frames.current.scope = ctx
	p.current = assignment{}
	return p.frames.writeCurrent()
}

func (p *metadataParser) setCurrentType(kind byte, pos Position) error {
	name, err := p.store.readName(p.current.name)
	if err != nil {
		return err
	}
	if name.kind != nameLeaf {
		return ErrIndexCorrupt
	}
	path := p.current.path
	if name.text.span.Start == name.text.span.End && p.current.context.path != 0 {
		path = p.current.context.path
	}
	return p.store.typeAt(path, kind, pos)
}
