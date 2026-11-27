package tomlinspect

const (
	frameRoot byte = iota + 1
	frameInline
	frameArray
)

type assignment struct {
	base, context              parseContext
	name, path                 uint32
	keys                       keySequence
	keyPosition, valuePosition Position
}

type parserFrame struct {
	kind       byte
	object     uint32
	scope      parseContext
	assignment assignment
}

func encodePosition(raw []byte, offset int, pos Position) {
	write32(raw, offset, uint32(pos.Line))
	write32(raw, offset+4, uint32(pos.Start))
	write32(raw, offset+8, uint32(pos.Len))
}
func decodePosition(raw []byte, offset int) Position {
	return Position{Line: int(read32(raw, offset)), Start: int(read32(raw, offset+4)), Len: int(read32(raw, offset+8))}
}
func encodeFrame(frame parserFrame) [80]byte {
	var raw [80]byte
	raw[0] = frame.kind
	write32(raw[:], 4, frame.object)
	write32(raw[:], 8, frame.scope.path)
	write32(raw[:], 12, frame.scope.object)
	a := frame.assignment
	write32(raw[:], 16, a.base.path)
	write32(raw[:], 20, a.base.object)
	write32(raw[:], 24, a.context.path)
	write32(raw[:], 28, a.context.object)
	write32(raw[:], 32, a.name)
	write32(raw[:], 36, a.path)
	write32(raw[:], 40, a.keys.start)
	write32(raw[:], 44, a.keys.count)
	encodePosition(raw[:], 48, a.keyPosition)
	encodePosition(raw[:], 60, a.valuePosition)
	return raw
}
func decodeFrame(raw []byte) (parserFrame, error) {
	if len(raw) != 80 || raw[0] < frameRoot || raw[0] > frameArray {
		return parserFrame{}, ErrIndexCorrupt
	}
	return parserFrame{kind: raw[0], object: read32(raw, 4), scope: parseContext{read32(raw, 8), read32(raw, 12)}, assignment: assignment{base: parseContext{read32(raw, 16), read32(raw, 20)}, context: parseContext{read32(raw, 24), read32(raw, 28)}, name: read32(raw, 32), path: read32(raw, 36), keys: keySequence{read32(raw, 40), read32(raw, 44)}, keyPosition: decodePosition(raw, 48), valuePosition: decodePosition(raw, 60)}}, nil
}

type parserFrames struct {
	store   *indexStore
	depth   uint32
	current parserFrame
}

func (f *parserFrames) push(frame parserFrame) error {
	if f.depth == ^uint32(0) {
		return ErrIndexInvariant
	}
	id := f.depth + 1
	raw := encodeFrame(frame)
	if id > f.store.classes[recordFrames].count {
		created, err := f.store.appendRecord(recordFrames, raw[:])
		if err != nil {
			return err
		}
		if created != id {
			return ErrIndexInvariant
		}
	} else if err := f.store.writeRecord(recordFrames, id, raw[:]); err != nil {
		return err
	}
	f.current, f.depth = frame, id
	return nil
}
func (f *parserFrames) writeCurrent() error {
	if f.depth == 0 {
		return ErrIndexInvariant
	}
	raw := encodeFrame(f.current)
	return f.store.writeRecord(recordFrames, f.depth, raw[:])
}
func (f *parserFrames) pop() (parserFrame, error) {
	if f.depth <= 1 {
		return parserFrame{}, ErrSyntax
	}
	old := f.current
	f.depth--
	var raw [80]byte
	if err := f.store.readRecord(recordFrames, f.depth, raw[:]); err != nil {
		return parserFrame{}, err
	}
	frame, err := decodeFrame(raw[:])
	if err != nil {
		return parserFrame{}, err
	}
	f.current = frame
	return old, nil
}
