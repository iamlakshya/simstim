package tomlinspect

import "encoding/binary"

func read64(data []byte, offset int) uint64 {
	return binary.LittleEndian.Uint64(data[offset : offset+8])
}
func write64(data []byte, offset int, value uint64) {
	binary.LittleEndian.PutUint64(data[offset:offset+8], value)
}
func putSpan(raw []byte, offset int, span Span) {
	write64(raw, offset, uint64(span.Start))
	write64(raw, offset+8, uint64(span.End))
}
func getSpan(raw []byte, offset int) Span {
	return Span{Start: int64(read64(raw, offset)), End: int64(read64(raw, offset+8))}
}

func (i *Inspector) metadataWriteAllowed() error {
	if i.outer.comparing {
		return i.scopeFailure(ErrIndexInvariant)
	}
	return nil
}
func (i *Inspector) appendMetadata(slot uint16, layout byte, raw []byte) (uint64, error) {
	if err := i.metadataWriteAllowed(); err != nil {
		return 0, err
	}
	book := &i.outer.books[slot]
	width := metadataRecordWidth(layout)
	if width == 0 || len(raw) != width {
		return 0, i.scopeFailure(ErrIndexInvariant)
	}
	if err := i.outer.regions.enter(book.ref); err != nil {
		return 0, i.scopeFailure(err)
	}
	defer i.outer.regions.leave(book.ref)
	state := &book.classes[layout]
	if uint64(state.count) >= i.recordLimit(layout) || state.count == ^uint32(0) {
		return 0, i.scopeFailure(ErrIndexInvariant)
	}
	state.count++
	id := state.count
	if err := i.metadataRecord(slot, layout, id, raw, true); err != nil {
		return 0, err
	}
	return uint64(id), nil
}
func (i *Inspector) readMetadata(slot uint16, layout byte, id uint64, raw []byte) error {
	book := &i.outer.books[slot]
	if id == 0 || id > uint64(book.classes[layout].count) || id > uint64(^uint32(0)) {
		return i.scopeFailure(ErrIndexCorrupt)
	}
	if err := i.outer.regions.enter(book.ref); err != nil {
		return i.scopeFailure(err)
	}
	defer i.outer.regions.leave(book.ref)
	return i.metadataRecord(slot, layout, uint32(id), raw, false)
}
func (i *Inspector) writeMetadata(slot uint16, layout byte, id uint64, raw []byte) error {
	if err := i.metadataWriteAllowed(); err != nil {
		return err
	}
	book := &i.outer.books[slot]
	if id == 0 || id > uint64(book.classes[layout].count) || id > uint64(^uint32(0)) {
		return i.scopeFailure(ErrIndexCorrupt)
	}
	if err := i.outer.regions.enter(book.ref); err != nil {
		return i.scopeFailure(err)
	}
	defer i.outer.regions.leave(book.ref)
	return i.metadataRecord(slot, layout, uint32(id), raw, true)
}
func (i *Inspector) metadataRecord(slot uint16, layout byte, id uint32, raw []byte, write bool) error {
	book := &i.outer.books[slot]
	width := metadataRecordWidth(layout)
	if width == 0 || len(raw) != width {
		return i.scopeFailure(ErrIndexInvariant)
	}
	perPage := uint32((indexPageBytes - indexPageHeader) / width)
	directory, err := i.outer.regions.directoryView(book.ref, layout, &book.classes[layout].root)
	if err != nil {
		return i.scopeFailure(err)
	}
	page, err := directory.page((id-1)/perPage, write)
	if err != nil {
		return i.scopeFailure(err)
	}
	data, pin, err := i.outer.regions.pin(book.ref, page)
	if err != nil {
		return i.scopeFailure(err)
	}
	if data[8] != pageRecordData || data[9] != layout {
		i.store.release(pin, false)
		return i.scopeFailure(ErrIndexCorrupt)
	}
	offset := indexPageHeader + int((id-1)%perPage)*width
	if write {
		copy(data[offset:offset+width], raw)
		i.store.counters.RecordWrites++
	} else {
		copy(raw, data[offset:offset+width])
		i.store.counters.RecordReads++
	}
	i.store.release(pin, write)
	return nil
}
