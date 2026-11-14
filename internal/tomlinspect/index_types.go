package tomlinspect

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
)

var (
	// ErrIndexInvariant means a metadata/source ownership or charge failed.
	ErrIndexInvariant = errors.New("owned TOML metadata invariant failed")
	// ErrIndexIO refuses a partial or failed private metadata operation.
	ErrIndexIO = errors.New("owned TOML metadata I/O failed")
	// ErrIndexCorrupt refuses an invalid private metadata page or reference.
	ErrIndexCorrupt = errors.New("owned TOML metadata is invalid")
)

const (
	indexPageBytes         = 4096
	indexCachePages        = 64
	indexCacheBytes        = indexPageBytes * indexCachePages
	indexPageHeader        = 32
	indexMagic      uint32 = 0x3149444d
	indexCRCOffset         = 12
)

const (
	pageDirectoryRoot byte = iota + 1
	pageDirectoryLeaf
	pageRecordData
	pageBTreeLeaf
	pageBTreeBranch
)

type pageTag struct {
	id    uint32
	used  uint64
	pins  uint16
	dirty bool
	held  bool
}

type indexCounters struct {
	PageReads, PageWrites, RecordReads, RecordWrites uint64
	SourceBytes, TrieVisits, NumericLookups, Splits  uint64
	Pages, PeakPins                                  uint64
	ReadBytes, WrittenBytes, RecordIOReads           uint64
}

type indexStore struct {
	ctx                context.Context
	file               IndexFile
	data               []byte
	tags               [indexCachePages]pageTag
	clock              uint64
	nextPage, maxPages uint32
	pins               uint64
	counters           indexCounters
	classes            [recordClassCount]recordClassState
	roots              [treeCount]uint32
	nameRoot           uint32
	nameDirectories    [10]uint32
	nameDirectoryCount int
	textSource         *source
}

func (s *indexStore) reset(ctx context.Context, file IndexFile, slab []byte, input *source) error {
	if input == nil {
		return ErrIndexInvariant
	}
	size := input.size
	if ctx == nil || file == nil || len(slab) != IndexWorkspaceBytes || size < 0 || size > MaxInput {
		return ErrIndexInvariant
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || info == nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return ErrIndexInvariant
	}
	*s = indexStore{ctx: ctx, file: file, data: slab[:indexCacheBytes], nextPage: 1, maxPages: uint32((256*size + (128 << 10) + indexPageBytes - 1) / indexPageBytes), textSource: input}
	return nil
}

func pageCRC(data []byte) uint32 {
	crc := crc32.Update(0, crc32.IEEETable, data[:indexCRCOffset])
	return crc32.Update(crc, crc32.IEEETable, data[indexCRCOffset+4:])
}
func read32(data []byte, offset int) uint32 {
	return binary.LittleEndian.Uint32(data[offset : offset+4])
}
func write32(data []byte, offset int, value uint32) {
	binary.LittleEndian.PutUint32(data[offset:offset+4], value)
}
func read16(data []byte, offset int) uint16 {
	return binary.LittleEndian.Uint16(data[offset : offset+2])
}
func write16(data []byte, offset int, value uint16) {
	binary.LittleEndian.PutUint16(data[offset:offset+2], value)
}
