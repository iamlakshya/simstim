package tomlinspect

func (s *StateRegion) AppendEntry(value SemanticEntry) (uint64, error) {
	i, err := s.check()
	if err != nil {
		return 0, err
	}
	if err := s.checkEntryValue(i, value); err != nil {
		return 0, err
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

// UpdateEntry replaces neutral metadata in the same checked source/phase.
// Callers must preserve indexed key spans. A reusable query entry must never
// be inserted as a durable tree key; flag-only updates preserve key identity.
func (s *StateRegion) UpdateEntry(id uint64, value SemanticEntry) error {
	i, err := s.check()
	if err != nil {
		return err
	}
	if err := s.checkEntryValue(i, value); err != nil {
		return err
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
	return i.writeMetadata(1, layoutEntry, id, raw[:])
}

func (s *StateRegion) checkEntryValue(i *Inspector, value SemanticEntry) error {
	for _, span := range []Span{value.Name, value.Sentinel, value.Identity, value.Prefix, value.Rule} {
		if err := s.span(span); err != nil {
			return err
		}
	}
	if value.BindingID > uint64(i.outer.books[1].classes[layoutBinding].count) {
		return i.scopeFailure(ErrIndexCorrupt)
	}
	if value.SlotCount != 0 && (value.SlotBase == 0 || value.SlotBase > uint64(i.outer.books[1].classes[layoutSlot].count) || value.SlotCount > uint64(i.outer.books[1].classes[layoutSlot].count)-value.SlotBase+1) {
		return i.scopeFailure(ErrIndexCorrupt)
	}
	return nil
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
	if err := s.checkHostValue(i, value); err != nil {
		return 0, err
	}
	var raw [96]byte
	write64(raw[:], 0, value.BindingID)
	putSpan(raw[:], 8, value.WholeSpan)
	putSpan(raw[:], 24, value.InteriorSpan)
	putSpan(raw[:], 40, value.ValueSpan)
	write64(raw[:], 56, value.ReferenceID)
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
	value := hostRecordValue(raw[:])
	if err := s.checkHostValue(i, value); err != nil {
		return HostRecord{}, err
	}
	return value, nil
}

// SetHostReference updates only the non-key binding reference. Indexed host
// grouping and source spans are immutable; no comparator may call this setter.
func (s *StateRegion) SetHostReference(id, reference uint64) error {
	i, err := s.check()
	if err != nil {
		return err
	}
	if err := i.metadataWriteAllowed(); err != nil {
		return err
	}
	if reference > uint64(i.outer.books[1].classes[layoutBinding].count) {
		return i.scopeFailure(ErrIndexCorrupt)
	}
	var raw [96]byte
	if err := i.readMetadata(1, layoutHost, id, raw[:]); err != nil {
		return err
	}
	if err := s.checkHostValue(i, hostRecordValue(raw[:])); err != nil {
		return err
	}
	write64(raw[:], 56, reference)
	return i.writeMetadata(1, layoutHost, id, raw[:])
}

func hostRecordValue(raw []byte) HostRecord {
	return HostRecord{
		BindingID: read64(raw, 0), WholeSpan: getSpan(raw, 8), InteriorSpan: getSpan(raw, 24),
		ValueSpan: getSpan(raw, 40), ReferenceID: read64(raw, 56),
	}
}

func (s *StateRegion) checkHostValue(i *Inspector, value HostRecord) error {
	bindings := uint64(i.outer.books[1].classes[layoutBinding].count)
	if value.BindingID == 0 || value.BindingID > bindings || value.ReferenceID > bindings || value.InteriorSpan.Start < value.WholeSpan.Start || value.InteriorSpan.End > value.WholeSpan.End {
		return i.scopeFailure(ErrIndexCorrupt)
	}
	for _, span := range [...]Span{value.WholeSpan, value.InteriorSpan, value.ValueSpan} {
		if err := s.span(span); err != nil {
			return err
		}
	}
	return nil
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
