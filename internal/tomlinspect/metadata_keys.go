package tomlinspect

type keySequence struct{ start, count uint32 }

func (s *indexStore) addKey(sequence *keySequence, name uint32) error {
	var raw [4]byte
	write32(raw[:], 0, name)
	id, err := s.appendRecord(recordKeys, raw[:])
	if err != nil {
		return err
	}
	if sequence.count == 0 {
		sequence.start = id
	} else if id != sequence.start+sequence.count {
		return ErrIndexInvariant
	}
	sequence.count++
	return nil
}
func (s *indexStore) keyAt(sequence keySequence, index uint32) (uint32, error) {
	if index >= sequence.count || sequence.start == 0 {
		return 0, ErrIndexInvariant
	}
	var raw [4]byte
	if err := s.readRecord(recordKeys, sequence.start+index, raw[:]); err != nil {
		return 0, err
	}
	name := read32(raw[:], 0)
	if name == 0 || name > s.classes[recordNames].count {
		return 0, ErrIndexCorrupt
	}
	return name, nil
}
func (s *indexStore) returnedValue(object uint32, sequence keySequence, value storedValue) error {
	if sequence.count == 0 {
		return ErrIndexInvariant
	}
	for index := uint32(0); index < sequence.count-1; index++ {
		name, err := s.keyAt(sequence, index)
		if err != nil {
			return err
		}
		child, found, err := s.mapValue(object, name)
		if err != nil {
			return err
		}
		if !found {
			child, err = s.newObject(storedMap, 0)
			if err != nil {
				return err
			}
			if err := s.setMapValue(object, name, child); err != nil {
				return err
			}
		}
		if child.kind != storedMap {
			return ErrSyntax
		}
		object = child.object
	}
	name, err := s.keyAt(sequence, sequence.count-1)
	if err != nil {
		return err
	}
	return s.setMapValue(object, name, value)
}
