package image

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/Microsoft/hcsshim/ext4/dmverity"
	"github.com/Microsoft/hcsshim/ext4/tar2ext4"
)

const scratchStart int64 = 32 << 30

// hcsshim's complete ext4 maximum includes filesystem metadata. Its verity
// tree has 128 SHA-256 digests per 4096-byte block, plus one superblock.
var maxOutput = convertedBound(MaxUncompressed)

func convertedBound(filesystem int64) int64 {
	blocks := filesystem / 4096
	bound := filesystem + 4096
	for {
		blocks = (blocks + 127) / 128
		bound += blocks * 4096
		if blocks <= 1 {
			return bound
		}
	}
}

type scratchFile struct{ file *os.File }

func (s scratchFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > MaxUncompressed || int64(len(p)) > MaxUncompressed-off {
		return 0, tooLarge("image scratch exceeds 16 GiB")
	}
	return s.file.ReadAt(p, scratchStart+off)
}
func (s scratchFile) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off > MaxUncompressed || int64(len(p)) > MaxUncompressed-off {
		return 0, tooLarge("image scratch exceeds 16 GiB")
	}
	return s.file.WriteAt(p, scratchStart+off)
}

// virtualFile hides scratch extents from tar2ext4's EOF and SeekEnd reads.
// A physical sparse file's size is never the length of the ext4 image.
type virtualFile struct {
	file             *os.File
	position, length int64
	limit            int64
	failure          func(string) error
}

func (v *virtualFile) maximum() int64 {
	if v.limit != 0 {
		return v.limit
	}
	return maxOutput
}
func (v *virtualFile) limitError(detail string) error {
	if v.failure != nil {
		return v.failure(detail)
	}
	return tooLarge("%s", detail)
}

func (v *virtualFile) Read(p []byte) (int, error) {
	if v.position >= v.length {
		return 0, io.EOF
	}
	if int64(len(p)) > v.length-v.position {
		p = p[:v.length-v.position]
	}
	n, err := v.file.ReadAt(p, v.position)
	v.position += int64(n)
	return n, err
}
func (v *virtualFile) Write(p []byte) (int, error) {
	if v.position < 0 || v.position > v.maximum() || int64(len(p)) > v.maximum()-v.position {
		return 0, v.limitError("converted image exceeds its output cap")
	}
	n, err := v.file.WriteAt(p, v.position)
	v.position += int64(n)
	if v.position > v.length {
		v.length = v.position
	}
	return n, err
}
func (v *virtualFile) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = v.position
	case io.SeekEnd:
		base = v.length
	default:
		return 0, fmt.Errorf("invalid image seek")
	}
	if offset < -base || offset > v.maximum()-base {
		return 0, v.limitError("image seek exceeds its output cap")
	}
	v.position = base + offset
	return v.position, nil
}

// Convert runs only inside the converter jail. It uses its output descriptor
// for bounded scratch storage and never opens a path supplied by a layer.
func Convert(ctx context.Context, layers []Layer, files []*os.File, out *os.File) (int, error) {
	if len(layers) != len(files) || len(layers) > MaxLayers {
		return 0, badLayer("converter received invalid layer descriptors")
	}
	if err := out.Truncate(0); err != nil {
		return 0, err
	}
	readers := make([]io.Reader, len(files))
	for i, file := range files {
		readers[i] = file
	}
	reader, writer := io.Pipe()
	type flattened struct {
		devices int
		err     error
	}
	done := make(chan flattened, 1)
	go func() {
		devices, err := Flatten(ctx, layers, readers, scratchFile{out}, writer)
		writer.CloseWithError(err)
		done <- flattened{devices, err}
	}()
	view := &virtualFile{file: out}
	err := tar2ext4.Convert(reader, view, tar2ext4.AppendDMVerity)
	reader.CloseWithError(err)
	result := <-done
	if result.err != nil {
		return result.devices, result.err
	}
	if err != nil {
		return result.devices, fmt.Errorf("convert image to ext4: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result.devices, err
	}
	if err := out.Truncate(view.length); err != nil {
		return result.devices, err
	}
	return result.devices, out.Sync()
}

// Inspect computes the root outside the jail from the first verity tree block.
// It derives the data block count from ext4, never from converter metadata.
func Inspect(file *os.File) (Record, error) {
	var record Record
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return record, err
	}
	size, block, err := tar2ext4.Ext4FileSystemSize(file)
	if err != nil {
		return record, fmt.Errorf("read converted ext4 superblock: %w", err)
	}
	st, err := file.Stat()
	if err != nil {
		return record, err
	}
	if block != 4096 || size <= 0 || size > MaxUncompressed || size%4096 != 0 || st.Size() < size+8192 || st.Size() > maxOutput {
		return record, badLayer("converted image has invalid ext4 or verity bounds")
	}
	first := make([]byte, 4096)
	if _, err := file.ReadAt(first, size+4096); err != nil {
		return record, fmt.Errorf("read converted verity tree: %w", err)
	}
	record.RootHash = hex.EncodeToString(dmverity.RootHash(first))
	record.DataBlocks = size / 4096
	record.Size = st.Size()
	return record, nil
}
