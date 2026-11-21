package supply

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
)

// CheckKernelEFI checks bytes, not runtime availability or boot authority.
// Linux amd64 needs a bzImage with the EFI stub; arm64 needs its raw EFI Image.
func CheckKernelEFI(arch string, data []byte) error {
	return checkKernelEFIReaderV1(context.Background(), arch, bytes.NewReader(data), int64(len(data)))
}

// The same checked descriptor is lent to a bounded header parser. It opens
// no path, changes no OFD offset and copies no complete kernel.
func checkKernelEFIItemV1(ctx context.Context, item Item, data io.ReaderAt) error {
	if item.Kind != KindKernel || item.Name != KernelEFIName {
		return ctx.Err()
	}
	return checkKernelEFIReaderV1(ctx, item.Arch, data, item.Size)
}

func checkKernelEFIReaderV1(ctx context.Context, arch string, data io.ReaderAt, size int64) error {
	if size < 0x218 {
		return fmt.Errorf("the EFI kernel has no bounded Linux PE header")
	}
	read := func(destination []byte, offset int64) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if offset < 0 || offset > size || int64(len(destination)) > size-offset {
			return fmt.Errorf("the EFI kernel header leaves its captured bytes")
		}
		n, err := data.ReadAt(destination, offset)
		if err != nil {
			return err
		}
		if n != len(destination) {
			return io.ErrUnexpectedEOF
		}
		return ctx.Err()
	}
	var linux [0x218]byte
	if err := read(linux[:], 0); err != nil {
		return err
	}
	if string(linux[:2]) != "MZ" {
		return fmt.Errorf("the EFI kernel has no Linux PE header")
	}
	machine := uint16(0x8664)
	switch arch {
	case "amd64":
		if binary.LittleEndian.Uint16(linux[0x1fe:]) != 0xaa55 || string(linux[0x202:0x206]) != "HdrS" || binary.LittleEndian.Uint16(linux[0x206:]) < 0x020b || linux[0x211]&1 == 0 {
			return fmt.Errorf("the amd64 EFI kernel is not a Linux bzImage EFI stub")
		}
	case "arm64":
		machine = 0xaa64
		if string(linux[0x38:0x3c]) != "ARMd" || binary.LittleEndian.Uint64(linux[0x10:]) == 0 {
			return fmt.Errorf("the arm64 EFI kernel is not a raw Linux EFI Image")
		}
	default:
		return fmt.Errorf("the EFI kernel architecture is unsupported")
	}
	var coff [24]byte
	peOffset := int64(binary.LittleEndian.Uint32(linux[0x3c:]))
	if err := read(coff[:], peOffset); err != nil {
		return err
	}
	count, optionalSize := int64(binary.LittleEndian.Uint16(coff[6:])), int64(binary.LittleEndian.Uint16(coff[20:]))
	if string(coff[:4]) != "PE\x00\x00" || binary.LittleEndian.Uint16(coff[4:]) != machine || optionalSize < 112 || count == 0 {
		return fmt.Errorf("the kernel PE image has another architecture or header")
	}
	var optional [112]byte
	if err := read(optional[:], peOffset+24); err != nil {
		return err
	}
	if binary.LittleEndian.Uint16(optional[:]) != 0x20b || binary.LittleEndian.Uint16(optional[68:]) != 10 {
		return fmt.Errorf("the kernel PE image has another EFI subsystem")
	}
	sectionOffset := peOffset + 24 + optionalSize
	if sectionOffset > size || count > (size-sectionOffset)/40 {
		return fmt.Errorf("the EFI section table leaves its captured bytes")
	}
	entry := uint64(binary.LittleEndian.Uint32(optional[16:]))
	// No symbol/debug tables are read or allocated. Only bounded section
	// headers can establish an executable entry in actual captured bytes.
	var section [40]byte
	for index := int64(0); index < count; index++ {
		if err := read(section[:], sectionOffset+40*index); err != nil {
			return err
		}
		address, length := uint64(binary.LittleEndian.Uint32(section[12:])), uint64(binary.LittleEndian.Uint32(section[16:]))
		offset, flags := uint64(binary.LittleEndian.Uint32(section[20:])), binary.LittleEndian.Uint32(section[36:])
		if flags&0x20000000 != 0 && entry >= address && entry-address < length && offset <= uint64(size) && length <= uint64(size)-offset {
			return ctx.Err()
		}
	}
	return fmt.Errorf("the EFI kernel entry has no actual executable section")
}
