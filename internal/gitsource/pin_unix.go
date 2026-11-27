//go:build linux || darwin

package gitsource

import (
	"github.com/iamlakshya/simstim/internal/files"
	"os"
)

func (a sourceAuthority) pinMetadataDirectory(name string) (*os.File, error) {
	return files.PinMount(name, false)
}
