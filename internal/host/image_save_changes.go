package host

import (
	"encoding/binary"
	"io"

	"github.com/iamlakshya/simstim/internal/image"
	"github.com/iamlakshya/simstim/internal/proto"
)

// The bounded raw prelude preserves the complete advisory vector without
// placing its 8 MiB in the existing 4 MiB JSON plan or in a disk spool.
func writeSavedChanges(output io.Writer, changes []string) error {
	if len(changes) > proto.ImageChangesMax {
		return imageChangesFailure()
	}
	if err := binary.Write(output, binary.LittleEndian, uint32(len(changes))); err != nil {
		return err
	}
	bytes := 0
	for _, name := range changes {
		bytes += len(name)
		if _, err := image.SaveRootPath(name); err != nil || bytes > proto.ImageChangesBytes {
			return imageChangesFailure()
		}
		if err := binary.Write(output, binary.LittleEndian, uint32(len(name))); err != nil {
			return err
		}
		count, err := io.WriteString(output, name)
		if err != nil {
			return err
		}
		if count != len(name) {
			return io.ErrShortWrite
		}
	}
	return nil
}

func readSavedChanges(input io.Reader) ([]string, error) {
	var count uint32
	if err := binary.Read(input, binary.LittleEndian, &count); err != nil {
		return nil, err
	}
	if count > proto.ImageChangesMax {
		return nil, imageChangesFailure()
	}
	changes := make([]string, 0, int(count))
	bytes := 0
	for range count {
		var length uint32
		if err := binary.Read(input, binary.LittleEndian, &length); err != nil {
			return nil, err
		}
		if length == 0 || length > 4096 || int(length) > proto.ImageChangesBytes-bytes {
			return nil, imageChangesFailure()
		}
		data := make([]byte, int(length))
		if _, err := io.ReadFull(input, data); err != nil {
			return nil, err
		}
		name := string(data)
		if _, err := image.SaveRootPath(name); err != nil {
			return nil, imageChangesFailure()
		}
		changes = append(changes, name)
		bytes += int(length)
	}
	return changes, nil
}

func imageChangesFailure() error {
	return UserError("image_protocol", "the original advisory changes exceed their path, count or aggregate bound", "Use the current helper on the same original image-save connection.")
}
