//go:build !linux

package image

import (
	"archive/tar"
	"os"
)

// The production exporter is Linux VM init. Other platforms run data tests.
func savedFileXattrs(_ *os.File, _ *tar.Header) error { return nil }
