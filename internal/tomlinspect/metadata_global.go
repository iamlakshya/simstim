package tomlinspect

type parseContext struct{ path, object uint32 }

// contextValue selects the actual latest table-array row once for this
// lexical scope. A scalar result is retained as object zero, so the pin's
// later parent traversal refuses it rather than pretending it became a map.
func (s *indexStore) contextValue(path uint32, value storedValue) (parseContext, error) {
	ctx := parseContext{path: path}
	switch value.kind {
	case storedMap:
		ctx.object = value.object
	case storedTableArray:
		info, err := s.object(value.object)
		if err != nil {
			return ctx, err
		}
		if info.kind != storedTableArray || info.count == 0 || info.lastMap == 0 {
			return ctx, ErrIndexCorrupt
		}
		ctx.object = info.lastMap
	}
	return ctx, nil
}

func (s *indexStore) globalValue(ctx parseContext, name, path uint32, value storedValue) (storedValue, error) {
	if ctx.object == 0 {
		return storedValue{}, ErrSyntax
	}
	previous, found, err := s.mapValue(ctx.object, name)
	if err != nil {
		return storedValue{}, err
	}
	if found {
		info, err := s.path(path)
		if err != nil {
			return storedValue{}, err
		}
		if info.kind == pathArray {
			if !info.implicit {
				return storedValue{}, ErrSyntax
			}
			info.implicit = false
			if err := s.writePath(path, info); err != nil {
				return storedValue{}, err
			}
		} else if info.implicit {
			info.implicit = false
			if err := s.writePath(path, info); err != nil {
				return storedValue{}, err
			}
			return previous, nil
		} else {
			return storedValue{}, ErrSyntax
		}
	}
	if err := s.setMapValue(ctx.object, name, value); err != nil {
		return storedValue{}, err
	}
	return value, nil
}

func (s *indexStore) headerParent(ctx parseContext, name, path uint32) (parseContext, error) {
	if ctx.object == 0 {
		return parseContext{}, ErrSyntax
	}
	value, found, err := s.mapValue(ctx.object, name)
	if err != nil {
		return parseContext{}, err
	}
	if !found {
		value, err = s.newObject(storedMap, 0)
		if err != nil {
			return parseContext{}, err
		}
		if err := s.implicit(path, true); err != nil {
			return parseContext{}, err
		}
		if err := s.setMapValue(ctx.object, name, value); err != nil {
			return parseContext{}, err
		}
	}
	selected, err := s.contextValue(path, value)
	if err == nil && selected.object == 0 {
		return selected, ErrSyntax
	}
	return selected, err
}

func (s *indexStore) implicitContext(ctx parseContext, name, path uint32) (parseContext, error) {
	if err := s.implicit(path, true); err != nil {
		return parseContext{}, err
	}
	fresh, err := s.newObject(storedMap, 0)
	if err != nil {
		return parseContext{}, err
	}
	actual, err := s.globalValue(ctx, name, path, fresh)
	if err != nil {
		return parseContext{}, err
	}
	return s.contextValue(path, actual)
}

func (s *indexStore) appendTable(ctx parseContext, name, path uint32) (parseContext, error) {
	if ctx.object == 0 {
		return parseContext{}, ErrSyntax
	}
	array, found, err := s.mapValue(ctx.object, name)
	if err != nil {
		return parseContext{}, err
	}
	if !found {
		array, err = s.newObject(storedTableArray, 0)
		if err != nil {
			return parseContext{}, err
		}
		if err := s.setMapValue(ctx.object, name, array); err != nil {
			return parseContext{}, err
		}
	}
	if array.kind != storedTableArray {
		return parseContext{}, ErrSyntax
	}
	fresh, err := s.newObject(storedMap, 0)
	if err != nil {
		return parseContext{}, err
	}
	if err := s.appendArray(array.object, fresh); err != nil {
		return parseContext{}, err
	}
	return parseContext{path: path, object: fresh.object}, nil
}

func nativePathType(it item) byte {
	switch it.typ {
	case itemString, itemStringEsc, itemRawString, itemMultilineString, itemRawMultilineString:
		return pathString
	case itemBool:
		return pathBool
	case itemInteger:
		return pathInteger
	case itemFloat:
		return pathFloat
	case itemDatetime:
		return pathDatetime
	default:
		return pathUnknown
	}
}
