// SPDX-License-Identifier: Apache-2.0
package jail

import (
	"encoding/binary"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"runtime"
)

// A synchronous held-code exec is an executable source only for a native ELF
// with no interpreter. Scripts and PT_INTERP would execute another code object.
// Read only fixed bounded headers; this is not a general executable parser.
func checkMappedControllerCodeV1(file *os.File) error {
	if err := checkMappedControllerSealedCodeV1(file); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return err
	}
	header := make([]byte, 64)
	if _, err := io.ReadFull(io.NewSectionReader(file, 0, 64), header); err != nil {
		return err
	}
	offset, count, err := mappedControllerELFExtentV1(header, info.Size(), runtime.GOARCH)
	if err != nil {
		return err
	}
	programs := make([]byte, count*56)
	if _, err := io.ReadFull(io.NewSectionReader(file, offset, int64(len(programs))), programs); err != nil {
		return err
	}
	return mappedControllerELFProgramsV1(header, programs, info.Size(), runtime.GOARCH)
}

// These bounded ELF shape readers are data validation only. They cannot issue
// an exec receipt; that still needs the real sealed held-code native syscall.
func mappedControllerELFExtentV1(header []byte, size int64, architecture string) (int64, int, error) {
	if len(header) != 64 || size < 64 || size > 512<<20 {
		return 0, 0, fmt.Errorf("closed native ELF header has another bounded extent")
	}
	count := int(binary.LittleEndian.Uint16(header[56:58]))
	offset := binary.LittleEndian.Uint64(header[32:40])
	machine := uint16(0)
	switch architecture {
	case "amd64":
		machine = 62
	case "arm64":
		machine = 183
	default:
		return 0, 0, fmt.Errorf("closed native role has another compiled architecture")
	}
	kind := binary.LittleEndian.Uint16(header[16:18])
	if string(header[:4]) != "\x7fELF" || header[4] != 2 || header[5] != 1 || header[6] != 1 || binary.LittleEndian.Uint32(header[20:24]) != 1 || (kind != 2 && kind != 3) || binary.LittleEndian.Uint16(header[18:20]) != machine || binary.LittleEndian.Uint16(header[52:54]) != 64 || binary.LittleEndian.Uint16(header[54:56]) != 56 || count < 1 || count > 256 || offset < 64 || offset > uint64(size) || uint64(count*56) > uint64(size)-offset {
		return 0, 0, fmt.Errorf("closed native role is not a bounded same-architecture ELF")
	}
	return int64(offset), count, nil
}

func mappedControllerELFProgramsV1(header, programs []byte, objectSize int64, architecture string) error {
	_, count, err := mappedControllerELFExtentV1(header, objectSize, architecture)
	if err != nil {
		return err
	}
	if len(programs) != count*56 {
		return fmt.Errorf("closed native ELF has another complete program-header extent")
	}
	entry := binary.LittleEndian.Uint64(header[24:32])
	executable := false
	for index := 0; index < count; index++ {
		ph := programs[index*56 : (index+1)*56]
		kind := binary.LittleEndian.Uint32(ph[:4])
		if kind == 3 {
			return fmt.Errorf("closed native role would execute an unbound ELF interpreter")
		}
		if kind != 1 {
			continue
		}
		off := binary.LittleEndian.Uint64(ph[8:16])
		segmentSize := binary.LittleEndian.Uint64(ph[32:40])
		memory := binary.LittleEndian.Uint64(ph[40:48])
		address := binary.LittleEndian.Uint64(ph[16:24])
		if off > uint64(objectSize) || segmentSize > uint64(objectSize)-off || memory < segmentSize {
			return fmt.Errorf("closed native role has another bounded load segment")
		}
		if binary.LittleEndian.Uint32(ph[4:8])&1 != 0 && entry >= address && entry-address < segmentSize {
			executable = true
		}
	}
	if !executable {
		return fmt.Errorf("closed native role entry is not in its same file-backed executable segment")
	}
	return nil
}

func checkMappedControllerSealedCodeV1(file *os.File) error {
	if file == nil {
		return fmt.Errorf("the original controller code is absent")
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil {
		return err
	}
	flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY || flags&unix.O_PATH != 0 || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0555 || st.Size < 64 || st.Size > 512<<20 {
		return fmt.Errorf("controller code changed its fixed sealed read-only executable shape: %v", err)
	}
	required := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&required != required {
		return fmt.Errorf("controller code lost immutable original bytes: %v", err)
	}
	return nil
}
