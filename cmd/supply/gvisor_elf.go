package main

import (
	"bytes"
	"debug/elf"
	"fmt"
)

func checkGvisorELFV1(data []byte, role, arch string) error {
	image, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer image.Close()
	machine := elf.EM_X86_64
	if arch == "arm64" {
		machine = elf.EM_AARCH64
	}
	if arch != "amd64" && arch != "arm64" || image.Class != elf.ELFCLASS64 || image.Data != elf.ELFDATA2LSB || image.Machine != machine || image.Type != elf.ET_EXEC && image.Type != elf.ET_DYN {
		return fmt.Errorf("gvisor %s is not its actual native Linux/%s ELF", role, arch)
	}
	executable := false
	for _, program := range image.Progs {
		if program.Type == elf.PT_INTERP {
			return fmt.Errorf("gvisor %s has an external ELF interpreter", role)
		}
		if program.Type == elf.PT_LOAD && program.Flags&elf.PF_X != 0 && image.Entry >= program.Vaddr && image.Entry-program.Vaddr < program.Filesz && program.Off <= uint64(len(data)) && program.Filesz <= uint64(len(data))-program.Off {
			executable = true
		}
	}
	if !executable {
		return fmt.Errorf("gvisor %s entry has no actual executable file segment", role)
	}
	if role != "runsc" && role != "gvisor_sentry" {
		return nil
	}
	symbols, err := image.Symbols()
	if err != nil {
		return fmt.Errorf("inspect actual gvisor %s guarded ELF entry: %w", role, err)
	}
	for _, symbol := range symbols {
		if symbol.Value == image.Entry && symbol.Section != elf.SHN_UNDEF && (symbol.Name == "gvisor.dev/gvisor/runsc/hostguard.entry" || symbol.Name == "gvisor.dev/gvisor/runsc/hostguard.entry.abi0") {
			return nil
		}
	}
	return fmt.Errorf("gvisor %s entry bypasses the pinned pre-init native memory guard", role)
}
