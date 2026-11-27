// SPDX-License-Identifier: Apache-2.0
package jail

import (
	"encoding/binary"
	"testing"
)

func TestMappedControllerRejectsScriptsInterpretersAndForeignELF(t *testing.T) {
	// Public synthetic headers validate shape only. No code is executed and no
	// process, sealed-code source or native exec receipt is constructed.
	header := make([]byte, 64)
	copy(header, "\x7fELF")
	header[4], header[5], header[6] = 2, 1, 1
	binary.LittleEndian.PutUint16(header[16:18], 2)
	binary.LittleEndian.PutUint16(header[18:20], 62)
	binary.LittleEndian.PutUint32(header[20:24], 1)
	binary.LittleEndian.PutUint64(header[24:32], 0x400040)
	binary.LittleEndian.PutUint64(header[32:40], 64)
	binary.LittleEndian.PutUint16(header[52:54], 64)
	binary.LittleEndian.PutUint16(header[54:56], 56)
	binary.LittleEndian.PutUint16(header[56:58], 1)
	programs := make([]byte, 56)
	binary.LittleEndian.PutUint32(programs[:4], 1)
	binary.LittleEndian.PutUint32(programs[4:8], 5)
	binary.LittleEndian.PutUint64(programs[16:24], 0x400000)
	binary.LittleEndian.PutUint64(programs[32:40], 120)
	binary.LittleEndian.PutUint64(programs[40:48], 120)
	if err := mappedControllerELFProgramsV1(header, programs, 120, "amd64"); err != nil {
		t.Fatal("bounded static public ELF shape rejected", err)
	}
	for _, change := range []func([]byte){
		func(h []byte) { copy(h, "#!/bin/sh\n") },
		func(h []byte) { h[4] = 1 },
		func(h []byte) { h[5] = 2 },
		func(h []byte) { binary.LittleEndian.PutUint16(h[18:20], 183) },
		func(h []byte) { binary.LittleEndian.PutUint16(h[16:18], 1) },
		func(h []byte) { binary.LittleEndian.PutUint16(h[54:56], 32) },
		func(h []byte) { binary.LittleEndian.PutUint16(h[56:58], 65535) },
		func(h []byte) { binary.LittleEndian.PutUint64(h[32:40], ^uint64(0)) },
		func(h []byte) { binary.LittleEndian.PutUint64(h[24:32], 0x500000) },
	} {
		candidate := append([]byte(nil), header...)
		change(candidate)
		if err := mappedControllerELFProgramsV1(candidate, programs, 120, "amd64"); err == nil {
			t.Fatal("script, foreign code or unbounded ELF became a closed native executable")
		}
	}
	for _, change := range []func([]byte){
		func(p []byte) { binary.LittleEndian.PutUint32(p[:4], 3) },
		func(p []byte) { binary.LittleEndian.PutUint32(p[4:8], 4) },
		func(p []byte) { binary.LittleEndian.PutUint64(p[8:16], ^uint64(0)) },
		func(p []byte) { binary.LittleEndian.PutUint64(p[32:40], 121) },
		func(p []byte) { binary.LittleEndian.PutUint64(p[40:48], 119) },
	} {
		candidate := append([]byte(nil), programs...)
		change(candidate)
		if err := mappedControllerELFProgramsV1(header, candidate, 120, "amd64"); err == nil {
			t.Fatal("interpreter or foreign entry source became a closed executable")
		}
	}
	if err := mappedControllerELFProgramsV1(header, programs[:55], 120, "amd64"); err == nil {
		t.Fatal("partial program-header read became a complete executable source")
	}
	if _, _, err := mappedControllerELFExtentV1(header[:63], 120, "amd64"); err == nil {
		t.Fatal("partial ELF header became an executable source")
	}
}
