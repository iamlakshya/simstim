package tomlinspect

// metadataParser executes the pinned parser over source-reference objects.
// It never retains a decoded key/value, component slice or Go depth stack.
type metadataParser struct {
	lx       *lexer
	store    *indexStore
	frames   parserFrames
	current  assignment
	root     storedValue
	position Position
}

func (p *metadataParser) next() (item, error) {
	it := p.lx.nextItem()
	if it.typ == itemError {
		return it, &Error{Position: it.pos, Cause: it.err}
	}
	return it, nil
}
func (p *metadataParser) expect(kind itemType) error {
	it, err := p.next()
	if err != nil {
		return err
	}
	if it.typ != kind {
		return &Error{Position: it.pos, Cause: ErrSyntax}
	}
	return nil
}
func (p *metadataParser) keyToken(it item) (uint32, error) {
	switch it.typ {
	case itemText, itemString, itemStringEsc, itemRawString, itemMultilineString, itemRawMultilineString:
		name, err := p.store.internName(Text{source: p.lx.input, span: it.val, kind: it.typ, generation: p.lx.input.generation})
		if err != nil {
			return 0, &Error{Position: it.pos, Cause: err}
		}
		return name, nil
	default:
		return 0, &Error{Position: it.pos, Cause: ErrSyntax}
	}
}
func (p *metadataParser) keys(end itemType) (keySequence, error) {
	var sequence keySequence
	for {
		it, err := p.next()
		if err != nil {
			return sequence, err
		}
		if sequence.count == 0 {
			p.position = it.pos
		}
		if it.typ == end {
			if sequence.count == 0 {
				return sequence, &Error{Position: it.pos, Cause: ErrSyntax}
			}
			return sequence, nil
		}
		name, err := p.keyToken(it)
		if err != nil {
			return sequence, err
		}
		if err := p.store.addKey(&sequence, name); err != nil {
			return sequence, err
		}
	}
}

func (p *metadataParser) run() (storedValue, error) {
	root, err := p.store.newObject(storedMap, 0)
	if err != nil {
		return storedValue{}, err
	}
	p.root = root
	p.frames = parserFrames{store: p.store}
	if err := p.frames.push(parserFrame{kind: frameRoot, object: root.object, scope: parseContext{object: root.object}}); err != nil {
		return storedValue{}, err
	}
	for {
		it, err := p.next()
		if err != nil {
			return storedValue{}, err
		}
		switch it.typ {
		case itemEOF:
			if p.frames.depth != 1 || p.frames.current.kind != frameRoot {
				return storedValue{}, ErrSyntax
			}
			return root, nil
		case itemCommentStart:
			if err := p.expect(itemText); err != nil {
				return storedValue{}, err
			}
		case itemTableStart, itemArrayTableStart:
			if p.frames.current.kind != frameRoot {
				return storedValue{}, ErrSyntax
			}
			if err := p.header(it); err != nil {
				return storedValue{}, err
			}
		case itemKeyStart:
			if p.frames.current.kind == frameArray {
				return storedValue{}, ErrSyntax
			}
			if err := p.assignment(it); err != nil {
				return storedValue{}, err
			}
			value, err := p.next()
			if err != nil {
				return storedValue{}, err
			}
			p.current.valuePosition = value.pos
			if err := p.value(value); err != nil {
				return storedValue{}, err
			}
		case itemArrayEnd:
			if p.frames.current.kind != frameArray {
				return storedValue{}, ErrSyntax
			}
			if err := p.closeValue(it, storedArray); err != nil {
				return storedValue{}, err
			}
		case itemInlineTableEnd:
			if p.frames.current.kind != frameInline {
				return storedValue{}, ErrSyntax
			}
			if err := p.closeValue(it, storedMap); err != nil {
				return storedValue{}, err
			}
		default:
			if p.frames.current.kind != frameArray {
				return storedValue{}, &Error{Position: it.pos, Cause: ErrSyntax}
			}
			if err := p.value(it); err != nil {
				return storedValue{}, err
			}
		}
	}
}
