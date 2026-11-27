package client

import (
	"bytes"
	"io"
	"os"
)

type nativeAgentProgram struct {
	file *os.File
	info os.FileInfo
}

func (p *nativeAgentProgram) Close() error { return p.file.Close() }

// openNativeAgentProgram retains the executable inode across Start. This
// rejects script wrappers and path replacement, not an untrusted local
// binary that deliberately imitates the trusted OpenSSH source contract.
func openNativeAgentProgram(path string) (*nativeAgentProgram, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, agentBindingProofError()
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, agentBindingProofError()
	}
	var header [4]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return nil, agentBindingProofError()
	}
	for _, magic := range [][]byte{
		{0x7f, 'E', 'L', 'F'},                              // ELF.
		{0xcf, 0xfa, 0xed, 0xfe}, {0xfe, 0xed, 0xfa, 0xcf}, // Mach-O 64.
		{0xca, 0xfe, 0xba, 0xbe}, {0xbe, 0xba, 0xfe, 0xca}, // Fat Mach-O.
	} {
		if bytes.Equal(header[:], magic) {
			keep = true
			return &nativeAgentProgram{file: file, info: info}, nil
		}
	}
	return nil, agentBindingProofError()
}

func sameNativeAgentProgram(program *nativeAgentProgram, path string) bool {
	if program == nil {
		return false
	}
	opened, err := program.file.Stat()
	current, statErr := os.Stat(path)
	return err == nil && statErr == nil && opened.Mode().IsRegular() &&
		current.Mode().IsRegular() && os.SameFile(program.info, opened) && os.SameFile(opened, current) &&
		program.info.Size() == current.Size() && program.info.ModTime() == current.ModTime()
}
