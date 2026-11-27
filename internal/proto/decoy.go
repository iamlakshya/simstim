package proto

import (
	"fmt"
	"io"
	"slices"
)

// DecoyFilesV1 is the one private start-generation payload. Guest paths, mode
// and account come only from the guest's embedded data and fixed helper role.
type DecoyFilesV1 struct {
	Version int           `json:"v"`
	Files   []DecoyFileV1 `json:"files"`
}

// DecoyFileV1 supplies rendered bytes for one canonical named decoy file.
// Content is a wire string like CtlStartArgs.Secrets, never a printable value.
type DecoyFileV1 struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// Format keeps complete generated file contents out of formatted output.
func (DecoyFileV1) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "[redacted]") }

// Format keeps complete generated file contents out of formatted output.
func (DecoyFilesV1) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "[redacted]") }

// CloneDecoyFilesV1 owns the private start's file list. Strings are immutable.
func CloneDecoyFilesV1(input *DecoyFilesV1) *DecoyFilesV1 {
	if input == nil {
		return nil
	}
	output := *input
	output.Files = slices.Clone(input.Files)
	return &output
}
