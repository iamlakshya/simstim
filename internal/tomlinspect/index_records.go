package tomlinspect

import "hash/crc32"

const (
	recordNames byte = iota
	recordObjects
	recordPaths
	recordKeys
	recordFrames
	recordMapOrder
	recordClassCount
)
const directorySlots = 1008

type recordClassState struct{ root, count uint32 }

func recordBytes(class byte) int {
	switch class {
	case recordNames, recordObjects, recordPaths:
		return 32
	case recordKeys:
		return 4
	case recordFrames:
		return 80
	case recordMapOrder:
		return 16
	default:
		return 0
	}
}

func (s *indexStore) retainNameDirectory(slot int) error {
	tag := &s.tags[slot]
	if tag.held {
		return nil
	}
	if s.nameDirectoryCount == len(s.nameDirectories) {
		return ErrIndexInvariant
	}
	tag.held = true
	tag.pins++
	s.pins++
	s.nameDirectories[s.nameDirectoryCount] = tag.id
	s.nameDirectoryCount++
	s.counters.PeakPins = max(s.counters.PeakPins, s.pins)
	return nil
}

func (s *indexStore) recordPage(class byte, virtual uint32, create bool) (uint32, error) {
	if class >= recordClassCount || virtual >= directorySlots*directorySlots {
		return 0, ErrIndexInvariant
	}
	state := &s.classes[class]
	var root []byte
	var slot int
	var err error
	if state.root == 0 {
		if !create {
			return 0, ErrIndexCorrupt
		}
		state.root, root, slot, err = s.allocate(pageDirectoryRoot, class)
	} else {
		root, slot, err = s.pin(state.root)
	}
	if err != nil {
		return 0, err
	}
	if root[8] != pageDirectoryRoot || root[9] != class {
		s.release(slot, false)
		return 0, ErrIndexCorrupt
	}
	if class == recordNames {
		if err := s.retainNameDirectory(slot); err != nil {
			s.release(slot, false)
			return 0, err
		}
	}
	offset := indexPageHeader + int(virtual/directorySlots)*4
	child := read32(root, offset)
	dirty := false
	if child == 0 && create {
		var next []byte
		var nextSlot int
		child, next, nextSlot, err = s.allocate(pageDirectoryLeaf, class)
		_ = next
		if err == nil {
			if class == recordNames {
				err = s.retainNameDirectory(nextSlot)
			}
			s.release(nextSlot, true)
		}
		if err != nil {
			s.release(slot, false)
			return 0, err
		}
		write32(root, offset, child)
		dirty = true
	}
	s.release(slot, dirty)
	if child == 0 {
		return 0, ErrIndexCorrupt
	}
	leaf, leafSlot, err := s.pin(child)
	if err != nil {
		return 0, err
	}
	if leaf[8] != pageDirectoryLeaf || leaf[9] != class {
		s.release(leafSlot, false)
		return 0, ErrIndexCorrupt
	}
	if class == recordNames {
		if err := s.retainNameDirectory(leafSlot); err != nil {
			s.release(leafSlot, false)
			return 0, err
		}
	}
	offset = indexPageHeader + int(virtual%directorySlots)*4
	page := read32(leaf, offset)
	dirty = false
	if page == 0 && create {
		var data []byte
		var dataSlot int
		page, data, dataSlot, err = s.allocate(pageRecordData, class)
		_ = data
		if err != nil {
			s.release(leafSlot, false)
			return 0, err
		}
		s.release(dataSlot, true)
		write32(leaf, offset, page)
		dirty = true
	}
	s.release(leafSlot, dirty)
	if page == 0 {
		return 0, ErrIndexCorrupt
	}
	return page, nil
}

func (s *indexStore) recordLocation(class byte, id uint32, create bool) (uint32, int, error) {
	width := recordBytes(class)
	if width == 0 || id == 0 || id > s.classes[class].count {
		return 0, 0, ErrIndexCorrupt
	}
	perPage := uint32((indexPageBytes - indexPageHeader) / width)
	page, err := s.recordPage(class, (id-1)/perPage, create)
	return page, indexPageHeader + int((id-1)%perPage)*width, err
}

func recordNameCRC(data []byte, id uint32) uint32 {
	var encoded [4]byte
	write32(encoded[:], 0, id)
	crc := crc32.Update(0, crc32.IEEETable, encoded[:])
	return crc32.Update(crc, crc32.IEEETable, data[:28])
}

func (s *indexStore) readRecord(class byte, id uint32, dst []byte) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	width := recordBytes(class)
	if len(dst) != width {
		return ErrIndexInvariant
	}
	page, offset, err := s.recordLocation(class, id, false)
	if err != nil {
		return err
	}
	// Name records have their own ID-bound checksum. A cold trie step
	// reads32bytes, rather than copying a whole unrelated4KiB page.
	if class == recordNames {
		cached := -1
		for slot := range s.tags {
			if s.tags[slot].id == page {
				cached = slot
				break
			}
		}
		if cached >= 0 {
			data, slot, err := s.pin(page)
			if err != nil {
				return err
			}
			if data[8] != pageRecordData || data[9] != class {
				s.release(slot, false)
				return ErrIndexCorrupt
			}
			copy(dst, data[offset:offset+width])
			s.release(slot, false)
		} else {
			n, err := s.file.ReadAt(dst, int64(page-1)*indexPageBytes+int64(offset))
			if n > 0 {
				s.counters.ReadBytes += uint64(n)
			}
			s.counters.RecordIOReads++
			if stopped := s.ctx.Err(); stopped != nil {
				return stopped
			}
			if err != nil || n != width {
				return ErrIndexIO
			}
		}
		if read32(dst, 28) != recordNameCRC(dst, id) {
			return ErrIndexCorrupt
		}
	} else {
		data, slot, err := s.pin(page)
		if err != nil {
			return err
		}
		if data[8] != pageRecordData || data[9] != class {
			s.release(slot, false)
			return ErrIndexCorrupt
		}
		copy(dst, data[offset:offset+width])
		s.release(slot, false)
	}
	s.counters.RecordReads++
	return nil
}

func (s *indexStore) writeRecord(class byte, id uint32, src []byte) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	width := recordBytes(class)
	if len(src) != width {
		return ErrIndexInvariant
	}
	page, offset, err := s.recordLocation(class, id, true)
	if err != nil {
		return err
	}
	data, slot, err := s.pin(page)
	if err != nil {
		return err
	}
	if data[8] != pageRecordData || data[9] != class {
		s.release(slot, false)
		return ErrIndexCorrupt
	}
	copy(data[offset:offset+width], src)
	if class == recordNames {
		write32(data, offset+28, recordNameCRC(data[offset:offset+width], id))
	}
	s.release(slot, true)
	s.counters.RecordWrites++
	return nil
}

func (s *indexStore) appendRecord(class byte, src []byte) (uint32, error) {
	if class >= recordClassCount || recordBytes(class) != len(src) {
		return 0, ErrIndexInvariant
	}
	if s.classes[class].count == ^uint32(0) {
		return 0, ErrIndexInvariant
	}
	s.classes[class].count++
	id := s.classes[class].count
	if err := s.writeRecord(class, id, src); err != nil {
		return 0, err
	}
	return id, nil
}
