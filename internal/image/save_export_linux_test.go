//go:build linux

package image

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSavedRootExporterKeepsOrdinaryDirectoryExtendedAttributes(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "ordinary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setxattr(filepath.Join(directory, "ordinary"), "user.saved", []byte("directory metadata"), 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) {
			t.Skip("the temporary filesystem has no user extended attributes")
		}
		t.Fatal(err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var output bytes.Buffer
	if err := WriteSavedRoot(t.Context(), root, savedTarPlan(t), &output); err != nil {
		t.Fatal(err)
	}
	_, entries := flatResult(t, output.Bytes())
	if entries["ordinary"].header.PAXRecords["SCHILY.xattr.user.saved"] != "directory metadata" {
		t.Fatal("directory attributes were lost before root conversion")
	}
}
