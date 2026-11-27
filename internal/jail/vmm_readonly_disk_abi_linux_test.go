//go:build linux

package jail

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
	"github.com/iamlakshya/simstim/internal/supply"
)

func TestReadonlyDiskActualFilesCannotReplaceOriginalSourceV2(t *testing.T) {
	dir := t.TempDir()
	parent, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	disk, err := os.OpenFile(filepath.Join(dir, "disk.img"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	if err := disk.Truncate(4096); err != nil {
		t.Fatal(err)
	}
	command := &Cmd{}
	source := &supply.ExecModeLeaseV1{}
	if err := command.CheckVMMReadonlyDiskV2(source, disk, parent); err == nil {
		t.Fatal("actual files without an original native source issued a disk proof")
	}
	if _, _, err := command.LoanVMMReadonlyDiskV2(context.Background(), source, nil, &proto.UnixControlV1{Files: []*os.File{disk, parent}}); err == nil {
		t.Fatal("literal ancillary metadata issued the original B receive witness")
	}
}
