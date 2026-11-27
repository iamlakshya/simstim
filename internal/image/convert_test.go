package image

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"
)

func convertFixture(t *testing.T) (*os.File, Record) {
	t.Helper()
	blob := flatTar(t, flatFixture{Name: "etc", Type: "directory"}, flatFixture{Name: "etc/config", Body: "deterministic\n"})
	layers, _ := flatInputs(blob)
	input, err := os.CreateTemp(t.TempDir(), "layer")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.Write(blob); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	output, err := os.CreateTemp(t.TempDir(), "root")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { output.Close() })
	if _, err := Convert(context.Background(), layers, []*os.File{input}, output); err != nil {
		t.Fatal(err)
	}
	record, err := Inspect(output)
	if err != nil {
		t.Fatal(err)
	}
	return output, record
}

func TestConvertDeterminismAndVerity(t *testing.T) {
	a, ar := convertFixture(t)
	b, br := convertFixture(t)
	if ar.RootHash != br.RootHash || ar.DataBlocks != br.DataBlocks || ar.Size != br.Size {
		t.Fatalf("conversion differs: %+v %+v", ar, br)
	}
	dataBytes := ar.DataBlocks * 4096
	left := make([]byte, dataBytes)
	right := make([]byte, dataBytes)
	if _, err := a.ReadAt(left, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ReadAt(right, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) {
		t.Fatal("ext4 data differs before random UUID superblock")
	}
	if ar.Size != convertedBound(dataBytes) {
		t.Fatalf("unexpected verity layout size %d for %d data bytes", ar.Size, dataBytes)
	}
}

func TestVirtualFileNeverReadsScratch(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "scratch")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scratch := scratchFile{file}
	if _, err := scratch.WriteAt([]byte("hostile scratch"), 0); err != nil {
		t.Fatal(err)
	}
	view := &virtualFile{file: file}
	if _, err := view.Write([]byte("ext4")); err != nil {
		t.Fatal(err)
	}
	end, err := view.Seek(0, io.SeekEnd)
	if err != nil || end != 4 {
		t.Fatalf("virtual end %d, %v", end, err)
	}
	if _, err := view.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("scratch became readable at EOF: %v", err)
	}
	if _, err := view.Seek(scratchStart, io.SeekStart); err == nil {
		t.Fatal("seek overlapped scratch")
	}
	if _, err := view.Seek(-5, io.SeekCurrent); err == nil {
		t.Fatal("negative seek accepted")
	}
	if maxOutput >= scratchStart || convertedBound(MaxUncompressed) != 17315151872 {
		t.Fatalf("scratch/layout arithmetic changed: %d", maxOutput)
	}
}
