package tomlinspect

func (p *metadataParser) value(it item) error {
	switch it.typ {
	case itemArray:
		if err := p.setCurrentType(pathArray, it.pos); err != nil {
			return err
		}
		value, err := p.store.newObject(storedArray, it.val.Start-1)
		if err != nil {
			return err
		}
		return p.frames.push(parserFrame{kind: frameArray, object: value.object, scope: p.current.context, assignment: p.current})
	case itemInlineTableStart:
		returned, err := p.store.newObject(storedMap, it.val.Start-1)
		if err != nil {
			return err
		}
		if err := p.store.implicit(p.current.path, true); err != nil {
			return err
		}
		var ctx parseContext
		if p.frames.current.kind == frameArray {
			ctx, err = p.store.appendTable(p.current.context, p.current.name, p.current.path)
		} else {
			global, failure := p.store.newObject(storedMap, 0)
			if failure != nil {
				return failure
			}
			actual, failure := p.store.globalValue(p.current.context, p.current.name, p.current.path, global)
			if failure != nil {
				return &Error{Position: p.position, Cause: failure}
			}
			ctx, err = p.store.contextValue(p.current.path, actual)
		}
		if err != nil {
			return &Error{Position: p.position, Cause: err}
		}
		return p.frames.push(parserFrame{kind: frameInline, object: returned.object, scope: ctx, assignment: p.current})
	case itemString, itemStringEsc, itemRawString, itemMultilineString, itemRawMultilineString, itemBool, itemInteger, itemFloat, itemDatetime:
		if _, err := nativeScalar(p.lx.input, it); err != nil {
			return &Error{Position: it.pos, Cause: err}
		}
		return p.complete(storedValue{kind: storedScalar, token: it.typ, span: it.val})
	default:
		return &Error{Position: it.pos, Cause: ErrSyntax}
	}
}

func (p *metadataParser) closeValue(it item, kind byte) error {
	value, err := p.store.finishObject(storedValue{kind: kind, object: p.frames.current.object}, it.val.End)
	if err != nil {
		return err
	}
	frame, err := p.frames.pop()
	if err != nil {
		return err
	}
	p.current = frame.assignment
	return p.complete(value)
}

func (p *metadataParser) complete(value storedValue) error {
	parent := p.frames.current
	if parent.kind == frameArray {
		return p.store.appendArray(parent.object, value)
	}
	if parent.kind != frameRoot && parent.kind != frameInline {
		return ErrIndexInvariant
	}
	if _, err := p.store.globalValue(p.current.context, p.current.name, p.current.path, value); err != nil {
		return &Error{Position: p.position, Cause: err}
	}
	kind := byte(pathUnknown)
	switch value.kind {
	case storedMap:
		kind = pathTable
	case storedArray:
		kind = pathArray
	case storedScalar:
		kind = nativePathType(item{typ: value.token})
	default:
		return ErrIndexInvariant
	}
	pos := p.current.valuePosition
	if parent.kind == frameInline {
		pos = p.current.keyPosition
	}
	if err := p.setCurrentType(kind, pos); err != nil {
		return err
	}
	if parent.kind == frameInline {
		if err := p.store.returnedValue(parent.object, p.current.keys, value); err != nil {
			return &Error{Position: p.position, Cause: err}
		}
	}
	return nil
}
