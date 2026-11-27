//go:build linux

package slotlaunch

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSparseSlotForkPreservesBytesHolesAndOriginalCursor(t *testing.T) {
	dir := t.TempDir()
	source, err := os.OpenFile(filepath.Join(dir, "source.disk"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(filepath.Join(dir, "target.disk"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	const size = 16 << 20
	if err := source.Truncate(size); err != nil {
		t.Fatal(err)
	}
	payload := []byte("original slot data")
	if _, err := source.WriteAt(payload, size-8192); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Seek(17, 0); err != nil {
		t.Fatal(err)
	}
	if err := copySparseForkDiskV1(t.Context(), source, target, size); err != nil {
		t.Fatal(err)
	}
	position, err := source.Seek(0, 1)
	if err != nil || position != 17 {
		t.Fatalf("fork changed the VMM's original file cursor: %d %v", position, err)
	}
	got := make([]byte, len(payload))
	if _, err := target.ReadAt(got, size-8192); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("fork data changed: %q %v", got, err)
	}
	zero := make([]byte, 4096)
	if _, err := target.ReadAt(zero, 4096); err != nil || !bytes.Equal(zero, make([]byte, len(zero))) {
		t.Fatal("fork did not preserve a sparse hole", err)
	}
	info, err := target.Stat()
	if err != nil || info.Size() != size {
		t.Fatal("fork changed the raw disk length", err)
	}
}

func TestSparseSlotForkStopsBeforeWritingWhenCancelled(t *testing.T) {
	dir := t.TempDir()
	source, err := os.OpenFile(filepath.Join(dir, "source.disk"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(filepath.Join(dir, "target.disk"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := copySparseForkDiskV1(ctx, source, target, 4096); err != context.Canceled {
		t.Fatal("cancelled fork was not cancelled", err)
	}
	info, err := target.Stat()
	if err != nil || info.Size() != 0 {
		t.Fatal("cancelled fork touched the destination", err)
	}
}

func TestSlotForkCannotOriginateFromCopiedSourceData(t *testing.T) {
	target, err := os.CreateTemp(t.TempDir(), "private-child")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	var source NativeSourceV1
	if err := source.CopyForkDiskToV1(t.Context(), target); err == nil {
		t.Fatal("a public zero source issued a slot fork copy")
	}
	info, err := target.Stat()
	if err != nil || info.Size() != 0 {
		t.Fatal("an invalid source wrote the actual child file", err)
	}
}
