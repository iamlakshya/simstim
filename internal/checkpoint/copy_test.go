package checkpoint

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/cachefs"
)

func TestSparseCopyPreservesHolesSizeBytesAndSourceOffset(t *testing.T) {
	f := newFixture(t)
	const size = 5<<20 + 7
	if err := f.disk.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if _, err := f.disk.WriteAt([]byte("head"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.disk.WriteAt([]byte("tail"), size-4); err != nil {
		t.Fatal(err)
	}
	if _, err := f.disk.Seek(2, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	target, err := cachefs.OpenFile(f.vm, "sparse-copy", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := sparseCopy(t.Context(), target, f.disk, size); err != nil {
		t.Fatal(err)
	}
	info, err := target.Stat()
	if err != nil || info.Size() != size {
		t.Fatalf("sparse copy size = %v, %v", info, err)
	}
	if offset, err := f.disk.Seek(0, io.SeekCurrent); err != nil || offset != 2 {
		t.Fatalf("copy changed the runtime's source offset: %d, %v", offset, err)
	}
	for _, item := range []struct {
		offset int64
		want   string
	}{{0, "head"}, {1 << 20, "\x00\x00\x00\x00"}, {size - 4, "tail"}} {
		data := make([]byte, len(item.want))
		if _, err := target.ReadAt(data, item.offset); err != nil || string(data) != item.want {
			t.Fatalf("copy at %d = %q, %v", item.offset, data, err)
		}
	}
}

func TestForkCopyCancellationLeavesExistingDestinationUntouched(t *testing.T) {
	f := newFixture(t)
	f.write(t, "source")
	existing, err := cachefs.OpenFile(f.vm, "child.disk", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	if _, err := existing.WriteString("existing child"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	checks := 0
	output, err := CopyOwned(ctx, f.vm, "child.disk", f.disk, func(context.Context) error {
		checks++
		if checks == 2 {
			cancel()
		}
		return ctx.Err()
	})
	if err == nil || output != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("copy accepted cancellation: %v, %v", output, err)
	}
	data, err := io.ReadAll(io.NewSectionReader(existing, 0, 100))
	if err != nil || string(data) != "existing child" {
		t.Fatalf("cancelled copy changed the existing destination: %q, %v", data, err)
	}
	dir, err := f.vm.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil || len(entries) != 2 {
		t.Fatalf("cancelled copy retained unfinished files: %v, %v", entries, err)
	}
}

func TestRealCloneProbeDoesNotLeaveFiles(t *testing.T) {
	f := newFixture(t)
	f.store.clone = cloneFile
	// A non-Linux host or unsupported filesystem may refuse the probe. The
	// assertion is that either result leaves only the factory's original disk.
	_ = f.store.Probe(t.Context())
	for root, count := range map[*os.Root]int{f.vm: 1, f.store.entries: 0} {
		dir, err := root.Open(".")
		if err != nil {
			t.Fatal(err)
		}
		entries, err := dir.ReadDir(-1)
		_ = dir.Close()
		if err != nil || len(entries) != count {
			t.Fatalf("clone probe retained files: %v, %v", entries, err)
		}
	}
}
