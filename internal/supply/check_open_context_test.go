package supply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Cancellation follows observed FD progress, not elapsed time or a mirrored
// count of context checks. The genuine Context cancel closes Done as usual.
type checkProgressContext struct {
	context.Context
	cancel           context.CancelFunc
	file             *os.File
	size, toolsBytes int64
	phase            string
	shaEnd, rewind   bool
	cancelledAt      int64
}

func (ctx *checkProgressContext) Err() error {
	if err := ctx.Context.Err(); err != nil {
		return err
	}
	position, err := ctx.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return ctx.Context.Err()
	}
	if ctx.phase == "sha" && position > 0 && position < ctx.size {
		ctx.cancelledAt = position
		ctx.cancel()
	}
	if ctx.phase == "tools" {
		if position == ctx.size {
			ctx.shaEnd = true
		}
		if ctx.shaEnd && position == 0 {
			ctx.rewind = true
		}
		if ctx.rewind && position > 0 && position <= ctx.toolsBytes {
			ctx.cancelledAt = position
			ctx.cancel()
		}
	}
	return ctx.Context.Err()
}

func progressContext(t *testing.T, file *os.File, phase string, size, toolsBytes int64) *checkProgressContext {
	t.Helper()
	parent, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	return &checkProgressContext{Context: parent, cancel: cancel, file: file, phase: phase, size: size, toolsBytes: toolsBytes}
}

func checkContextFile(t *testing.T, root string, data []byte) *os.File {
	t.Helper()
	file := receiverFile(t, root)
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	return file
}

func assertContextDescriptorUntouched(t *testing.T, file *os.File, before os.FileInfo, data []byte) {
	t.Helper()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Mode() != before.Mode() || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("verification closed or changed its caller's descriptor", err)
	}
	got := make([]byte, len(data))
	if _, err := file.ReadAt(got, 0); err != nil || !bytes.Equal(got, data) {
		t.Fatal("verification changed actual caller bytes", err)
	}
	if named, err := os.Stat(file.Name()); err != nil || !os.SameFile(before, named) {
		t.Fatal("verification unlinked or published its caller's file", err)
	}
}

func TestCheckOpenContextCancellationDuringActualSHAReads(t *testing.T) {
	root := isolateReceiver(t)
	data := bytes.Repeat([]byte("hash bytes\x00\n"), 20000)
	file := checkContextFile(t, root, data)
	before, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	ctx := progressContext(t, file, "sha", int64(len(data)), 0)
	if err := CheckOpenContext(ctx, receiverItem(data), file); !errors.Is(err, context.Canceled) {
		t.Fatal("SHA reader did not observe cancellation after actual partial progress", err)
	}
	if ctx.cancelledAt <= 0 || ctx.cancelledAt >= int64(len(data)) {
		t.Fatal("fixture never cancelled during partial SHA input", ctx.cancelledAt)
	}
	assertContextDescriptorUntouched(t, file, before, data)
}

func TestCheckOpenContextCancellationDuringActualToolsReader(t *testing.T) {
	root := isolateReceiver(t)
	item, data := verityTools(t)
	file := checkContextFile(t, root, data)
	before, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	ctx := progressContext(t, file, "tools", int64(len(data)), item.DataBlocks*4096)
	if err := CheckOpenContext(ctx, item, file); !errors.Is(err, context.Canceled) {
		t.Fatal("tools data reader lost parent cancellation", err)
	}
	if !ctx.shaEnd || !ctx.rewind || ctx.cancelledAt <= 0 || ctx.cancelledAt > item.DataBlocks*4096 {
		t.Fatalf("fixture did not pass whole-file SHA then cancel in actual tools data: %+v", ctx)
	}
	assertContextDescriptorUntouched(t, file, before, data)
}

func TestCheckOpenContextPreCancelledLeavesOriginalSeekPositionAndFD(t *testing.T) {
	root := isolateReceiver(t)
	data := []byte("caller bytes remain")
	file := checkContextFile(t, root, data)
	if _, err := file.Seek(7, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := CheckOpenContext(ctx, receiverItem(data), file); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if position, err := file.Seek(0, io.SeekCurrent); err != nil || position != 7 {
		t.Fatal("cancelled entry performed a seek or closed the FD", position, err)
	}
}

func TestCheckOpenContextPreservesCanonicalTypedMismatch(t *testing.T) {
	root := isolateReceiver(t)
	data := []byte("abc")
	const actualABC = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	for _, what := range []string{"size", "SHA-256"} {
		file := checkContextFile(t, root, data)
		item := receiverItem(data)
		want, got := "4 bytes", "3 bytes"
		if what == "size" {
			item.Size = 4
		} else {
			item.SHA256 = strings.Repeat("f", 64)
			want, got = item.SHA256, actualABC
		}
		var mismatch *MismatchError
		if err := CheckOpenContext(t.Context(), item, file); !errors.As(err, &mismatch) || mismatch.Name != item.Name || mismatch.What != what || mismatch.Want != want || mismatch.Got != got {
			t.Fatalf("canonical mismatch contract changed: %+v, %v", mismatch, err)
		}
		if _, err := file.WriteAt([]byte("z"), 0); err != nil {
			t.Fatal("mismatch closed caller descriptor", err)
		}
	}
}

func TestCheckOpenContextKeepsRegularAndSingleLinkRefusals(t *testing.T) {
	root := isolateReceiver(t)
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	if err := CheckOpenContext(t.Context(), receiverItem([]byte("abc")), directory); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatal("directory became a runtime file", err)
	}
	file := checkContextFile(t, root, []byte("abc"))
	alias := filepath.Join(root, "foreign-link")
	if err := os.Link(file.Name(), alias); err != nil {
		t.Fatal(err)
	}
	if err := CheckOpenContext(t.Context(), receiverItem([]byte("abc")), file); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatal("multi-link descriptor became a runtime file", err)
	}
	for _, name := range []string{file.Name(), alias} {
		if data, err := os.ReadFile(name); err != nil || string(data) != "abc" {
			t.Fatal("refusal cleaned foreign or caller link", name, err)
		}
	}
}

func TestCheckOpenContextDoesNotSkipCanonicalToolsStoredTree(t *testing.T) {
	root := isolateReceiver(t)
	item, data := verityTools(t)
	data[len(data)-1] ^= 1
	// Keep the independent whole-file digest coherent. Only the canonical
	// tools tree verifier can reject this stored-tree corruption.
	sum := sha256.Sum256(data)
	item.SHA256 = hex.EncodeToString(sum[:])
	file := checkContextFile(t, root, data)
	if err := CheckOpenContext(t.Context(), item, file); err == nil || !strings.Contains(err.Error(), "verity tree") {
		t.Fatal("context path lost canonical tools tree verification", err)
	}
}

func TestReceiverFinishContextOwnsNoCleanupPermissionsOrPublication(t *testing.T) {
	for _, phase := range []string{"cancel-before", "cancel-during", "success"} {
		t.Run(phase, func(t *testing.T) {
			root := isolateReceiver(t)
			data := bytes.Repeat([]byte("receiver\x00\n"), 20000)
			file := receiverFile(t, root)
			receiver, err := NewReceiver(receiverItem(data), file)
			if err != nil {
				t.Fatal(err)
			}
			receiverWriteAll(t, receiver, data)
			before, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			var ctx context.Context = t.Context()
			if phase == "cancel-before" {
				owned, cancel := context.WithCancel(ctx)
				cancel()
				ctx = owned
			} else if phase == "cancel-during" {
				ctx = progressContext(t, file, "sha", int64(len(data)), 0)
			}
			err = receiver.FinishContext(ctx)
			if phase == "success" && err != nil || phase != "success" && !errors.Is(err, context.Canceled) {
				t.Fatal("finish outcome", phase, err)
			}
			if receiver.Received() != int64(len(data)) {
				t.Fatal("verification changed accepted progress")
			}
			assertContextDescriptorUntouched(t, file, before, data)
			after, err := os.ReadDir(root)
			if err != nil || len(after) != len(entries) {
				t.Fatal("receiver created a publication or removed another entry", err)
			}
			for index := range entries {
				if entries[index].Name() != after[index].Name() {
					t.Fatal("receiver renamed/published a namespace entry")
				}
			}
			if _, err := file.WriteAt([]byte("x"), 0); err != nil {
				t.Fatal("receiver took descriptor lifetime or write authority", err)
			}
		})
	}
}

type cancellingReadSeeker struct {
	reader *bytes.Reader
	cancel context.CancelFunc
	onRead bool
}

func (reader cancellingReadSeeker) Read(data []byte) (int, error) {
	n, err := reader.reader.Read(data)
	if reader.onRead {
		reader.cancel()
	}
	return n, err
}
func (reader cancellingReadSeeker) Seek(offset int64, whence int) (int64, error) {
	position, err := reader.reader.Seek(offset, whence)
	if !reader.onRead {
		reader.cancel()
	}
	return position, err
}

func TestContextualReadSeekerChecksParentAfterUnderlyingReadAndSeek(t *testing.T) {
	isolateReceiver(t)
	for _, operation := range []string{"read", "seek"} {
		ctx, cancel := context.WithCancel(t.Context())
		underlying := cancellingReadSeeker{reader: bytes.NewReader([]byte("abc")), cancel: cancel, onRead: operation == "read"}
		wrapped := contextualReadSeeker{ctx: ctx, file: underlying}
		if operation == "read" {
			count, err := wrapped.Read(make([]byte, 3))
			if count != 3 || !errors.Is(err, context.Canceled) {
				t.Fatal("read result hid cancellation raised inside actual read", count, err)
			}
		} else {
			position, err := wrapped.Seek(2, io.SeekStart)
			if position != 2 || !errors.Is(err, context.Canceled) {
				t.Fatal("seek result hid cancellation raised inside actual seek", position, err)
			}
		}
		cancel()
	}
}

func TestReceiverFinishContextRetainsCanonicalMismatchAndCallerFiles(t *testing.T) {
	root := isolateReceiver(t)
	file := receiverFile(t, root)
	receiver, err := NewReceiver(receiverItem([]byte("abc")), file)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteAt([]byte("abd"), 0); err != nil {
		t.Fatal(err)
	}
	var mismatch *MismatchError
	if err := receiver.FinishContext(t.Context()); !errors.As(err, &mismatch) || mismatch.What != "SHA-256" || mismatch.Want != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal("context receiver hid canonical typed checksum mismatch", err)
	}
	if got, err := os.ReadFile(file.Name()); err != nil || string(got) != "abd" {
		t.Fatal("mismatch cleanup changed or removed caller bytes", err)
	}
	if _, err := file.WriteAt([]byte("x"), 0); err != nil {
		t.Fatal("checksum mismatch closed caller FD", err)
	}
}
