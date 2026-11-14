package tomlinspect

func (s *StateRegion) AppendEntry(value SemanticEntry) (uint64, error) {
	i, err := s.check()
	if err != nil {
		return 0, err
	}
	for _, span := range []Span{value.Name, value.Sentinel, value.Identity, value.Prefix, value.Rule} {
		if err := s.span(span); err != nil {
			return 0, err
		}
	}
	if value.BindingID > uint64(i.outer.books[1].classes[layoutBinding].count) {
		return 0, i.scopeFailure(ErrIndexCorrupt)
	}
	if value.SlotCount != 0 && (value.SlotBase == 0 || value.SlotBase > uint64(i.outer.books[1].classes[layoutSlot].count) || value.SlotCount > uint64(i.outer.books[1].classes[layoutSlot].count)-value.SlotBase+1) {
		return 0, i.scopeFailure(ErrIndexCorrupt)
	}
	var raw [192]byte
	raw[0] = value.Kind
	if value.Armed {
		raw[1] = 1
	}
	putSpan(raw[:], 8, value.Name)
	putSpan(raw[:], 24, value.Sentinel)
	putSpan(raw[:], 40, value.Identity)
	putSpan(raw[:], 56, value.Prefix)
	putSpan(raw[:], 72, value.Rule)
	write64(raw[:], 88, value.BindingID)
	write64(raw[:], 96, value.SlotBase)
	write64(raw[:], 104, value.SlotCount)
	return i.appendMetadata(1, layoutEntry, raw[:])
}
func (s *StateRegion) Entry(id uint64) (SemanticEntry, error) {
	i, err := s.check()
	if err != nil {
		return SemanticEntry{}, err
	}
	var raw [192]byte
	if err = i.readMetadata(1, layoutEntry, id, raw[:]); err != nil {
		return SemanticEntry{}, err
	}
	if raw[1] > 1 {
		return SemanticEntry{}, i.scopeFailure(ErrIndexCorrupt)
	}
	value := SemanticEntry{Kind: raw[0], Armed: raw[1] == 1, Name: getSpan(raw[:], 8), Sentinel: getSpan(raw[:], 24), Identity: getSpan(raw[:], 40), Prefix: getSpan(raw[:], 56), Rule: getSpan(raw[:], 72), BindingID: read64(raw[:], 88), SlotBase: read64(raw[:], 96), SlotCount: read64(raw[:], 104)}
	for _, span := range []Span{value.Name, value.Sentinel, value.Identity, value.Prefix, value.Rule} {
		if err := s.span(span); err != nil {
			return SemanticEntry{}, err
		}
	}
	if value.BindingID > uint64(i.outer.books[1].classes[layoutBinding].count) {
		return SemanticEntry{}, i.scopeFailure(ErrIndexCorrupt)
	}
	if value.SlotCount != 0 && (value.SlotBase == 0 || value.SlotBase > uint64(i.outer.books[1].classes[layoutSlot].count) || value.SlotCount > uint64(i.outer.books[1].classes[layoutSlot].count)-value.SlotBase+1) {
		return SemanticEntry{}, i.scopeFailure(ErrIndexCorrupt)
	}
	return value, nil
}
func (s *StateRegion) AppendBinding(value BindingRecord) (uint64, error) {
	i, err := s.check()
	if err != nil {
		return 0, err
	}
	for _, span := range []Span{value.Name, value.Header, value.Format} {
		if err := s.span(span); err != nil {
			return 0, err
		}
	}
	if value.HostCount > i.outer.stateCounts.hosts {
		return 0, i.scopeFailure(ErrIndexInvariant)
	}
	var raw [192]byte
	raw[0] = value.FormatKind
	putSpan(raw[:], 8, value.Name)
	putSpan(raw[:], 24, value.Header)
	putSpan(raw[:], 40, value.Format)
	write64(raw[:], 56, value.HostCount)
	write64(raw[:], 64, value.UserBytes)
	write64(raw[:], 72, value.HeaderBytes)
	return i.appendMetadata(1, layoutBinding, raw[:])
}
func (s *StateRegion) Binding(id uint64) (BindingRecord, error) {
	i, err := s.check()
	if err != nil {
		return BindingRecord{}, err
	}
	var raw [192]byte
	if err = i.readMetadata(1, layoutBinding, id, raw[:]); err != nil {
		return BindingRecord{}, err
	}
	value := BindingRecord{FormatKind: raw[0], Name: getSpan(raw[:], 8), Header: getSpan(raw[:], 24), Format: getSpan(raw[:], 40), HostCount: read64(raw[:], 56), UserBytes: read64(raw[:], 64), HeaderBytes: read64(raw[:], 72)}
	for _, span := range []Span{value.Name, value.Header, value.Format} {
		if err := s.span(span); err != nil {
			return BindingRecord{}, err
		}
	}
	if value.HostCount > i.outer.stateCounts.hosts {
		return BindingRecord{}, i.scopeFailure(ErrIndexCorrupt)
	}
	return value, nil
}
func (s *StateRegion) SetBindingHostCount(id, count uint64) error {
	i, err := s.check()
	if err != nil {
		return err
	}
	if count > i.outer.stateCounts.hosts {
		return i.scopeFailure(ErrIndexInvariant)
	}
	var raw [192]byte
	if err = i.readMetadata(1, layoutBinding, id, raw[:]); err != nil {
		return err
	}
	write64(raw[:], 56, count)
	return i.writeMetadata(1, layoutBinding, id, raw[:])
}
func (s *StateRegion) AppendHost(value HostRecord) (uint64, error) {
	i, err := s.check()
	if err != nil {
		return 0, err
	}
	if value.BindingID == 0 || value.BindingID > uint64(i.outer.books[1].classes[layoutBinding].count) || value.InteriorSpan.Start < value.WholeSpan.Start || value.InteriorSpan.End > value.WholeSpan.End {
		return 0, i.scopeFailure(ErrIndexCorrupt)
	}
	if err = s.span(value.WholeSpan); err != nil {
		return 0, err
	}
	if err = s.span(value.InteriorSpan); err != nil {
		return 0, err
	}
	var raw [96]byte
	write64(raw[:], 0, value.BindingID)
	putSpan(raw[:], 8, value.WholeSpan)
	putSpan(raw[:], 24, value.InteriorSpan)
	return i.appendMetadata(1, layoutHost, raw[:])
}
func (s *StateRegion) Host(id uint64) (HostRecord, error) {
	i, err := s.check()
	if err != nil {
		return HostRecord{}, err
	}
	var raw [96]byte
	if err = i.readMetadata(1, layoutHost, id, raw[:]); err != nil {
		return HostRecord{}, err
	}
	value := HostRecord{BindingID: read64(raw[:], 0), WholeSpan: getSpan(raw[:], 8), InteriorSpan: getSpan(raw[:], 24)}
	if value.BindingID == 0 || value.BindingID > uint64(i.outer.books[1].classes[layoutBinding].count) || value.InteriorSpan.Start < value.WholeSpan.Start || value.InteriorSpan.End > value.WholeSpan.End {
		return HostRecord{}, i.scopeFailure(ErrIndexCorrupt)
	}
	if err = s.span(value.WholeSpan); err != nil {
		return HostRecord{}, err
	}
	if err = s.span(value.InteriorSpan); err != nil {
		return HostRecord{}, err
	}
	return value, nil
}
func (s *StateRegion) AppendSlot(value SlotRecord) (uint64, error) {
	i, err := s.check()
	if err != nil {
		return 0, err
	}
	if err = s.span(value.ASCIISpan); err != nil {
		return 0, err
	}
	var raw [96]byte
	raw[0], raw[1] = value.Kind, value.Width
	write64(raw[:], 8, value.Count)
	putSpan(raw[:], 16, value.ASCIISpan)
	return i.appendMetadata(1, layoutSlot, raw[:])
}
func (s *StateRegion) Slot(ordinal uint64) (SlotRecord, error) {
	i, err := s.check()
	if err != nil {
		return SlotRecord{}, err
	}
	var raw [96]byte
	if err = i.readMetadata(1, layoutSlot, ordinal, raw[:]); err != nil {
		return SlotRecord{}, err
	}
	value := SlotRecord{Kind: raw[0], Width: raw[1], Count: read64(raw[:], 8), ASCIISpan: getSpan(raw[:], 16)}
	if err = s.span(value.ASCIISpan); err != nil {
		return SlotRecord{}, err
	}
	return value, nil
}
