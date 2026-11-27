package supply

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateReceiver(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		dir := filepath.Join(root, key)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
	return root
}
func receiverFile(t *testing.T, root string) *os.File {
	t.Helper()
	file, err := os.CreateTemp(root, "receiver-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}
func receiverItem(data []byte) Item {
	return NewItem(KindUserns, "namespace", "1.0.0", "amd64", data, ReleaseFolder("0.6.0")+"/namespace-amd64")
}
func receiverBytes(t *testing.T, file *os.File) []byte {
	t.Helper()
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func receiverWriteAll(t *testing.T, receiver *Receiver, data []byte) {
	t.Helper()
	for offset := 0; offset < len(data); {
		end := min(offset+TransferChunkSize, len(data))
		if err := receiver.WriteAt(data[offset:end], int64(offset)); err != nil {
			t.Fatal(err)
		}
		offset = end
	}
}

func TestReceiverAcceptsExactChunksWithoutUsingSeekPosition(t *testing.T) {
	root := isolateReceiver(t)
	data := bytes.Repeat([]byte("x"), 2*TransferChunkSize+13)
	file := receiverFile(t, root)
	if _, err := file.Seek(123, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	receiver, err := NewReceiver(receiverItem(data), file)
	if err != nil {
		t.Fatal(err)
	}
	receiverWriteAll(t, receiver, data)
	if receiver.Received() != int64(len(data)) || !bytes.Equal(receiverBytes(t, file), data) {
		t.Fatal("transfer changed size or bytes")
	}
	if position, err := file.Seek(0, io.SeekCurrent); err != nil || position != 123 {
		t.Fatalf("shared descriptor offset changed: %d, %v", position, err)
	}
	if err := receiver.Finish(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("z"), 0); err != nil {
		t.Fatalf("receiver closed or restricted its caller's descriptor: %v", err)
	}
	if got := receiverBytes(t, file); len(got) != len(data) || got[0] != 'z' {
		t.Fatal("receiver removed its caller's file")
	}
}

func TestReceiverRejectsInvalidChunksBeforeWritingAndCannotResume(t *testing.T) {
	for _, test := range []struct {
		name   string
		data   []byte
		offset int64
	}{
		{"replay", []byte("c"), 0}, {"gap", []byte("c"), 3}, {"negative", []byte("c"), -1},
		{"empty", nil, 2}, {"declared overflow", []byte("cdefg"), 2},
		{"chunk overflow", bytes.Repeat([]byte("x"), TransferChunkSize+1), 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := isolateReceiver(t)
			file := receiverFile(t, root)
			receiver, err := NewReceiver(receiverItem([]byte("abcdef")), file)
			if err != nil {
				t.Fatal(err)
			}
			if err := receiver.WriteAt([]byte("ab"), 0); err != nil {
				t.Fatal(err)
			}
			if err := receiver.WriteAt(test.data, test.offset); err == nil {
				t.Fatal("accepted an invalid chunk")
			}
			if receiver.Received() != 2 || !bytes.Equal(receiverBytes(t, file), []byte("ab")) {
				t.Fatal("invalid chunk wrote bytes or advanced progress")
			}
			if err := receiver.WriteAt([]byte("cdef"), 2); err == nil {
				t.Fatal("failed receiver resumed")
			}
			if err := receiver.Finish(); err == nil {
				t.Fatal("failed receiver finished")
			}
			if !bytes.Equal(receiverBytes(t, file), []byte("ab")) {
				t.Fatal("failure cleaned up or replaced the caller's file")
			}
		})
	}
}

func TestReceiverRefusesDescriptorMutationBetweenChunks(t *testing.T) {
	for _, mutation := range []string{"truncate", "append", "hardlink"} {
		t.Run(mutation, func(t *testing.T) {
			root := isolateReceiver(t)
			file := receiverFile(t, root)
			receiver, err := NewReceiver(receiverItem([]byte("abcdef")), file)
			if err != nil {
				t.Fatal(err)
			}
			if err := receiver.WriteAt([]byte("ab"), 0); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "truncate":
				if err := file.Truncate(1); err != nil {
					t.Fatal(err)
				}
			case "append":
				if _, err := file.WriteAt([]byte("foreign"), 2); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(file.Name(), filepath.Join(root, "foreign-link")); err != nil {
					t.Fatal(err)
				}
			}
			before := receiverBytes(t, file)
			if err := receiver.WriteAt([]byte("cd"), 2); err == nil {
				t.Fatal("accepted a changed descriptor")
			}
			if receiver.Received() != 2 || !bytes.Equal(receiverBytes(t, file), before) {
				t.Fatal("receiver changed foreign bytes or advanced progress")
			}
			if _, err := file.Stat(); err != nil {
				t.Fatalf("receiver closed the changed descriptor: %v", err)
			}
		})
	}
}

func TestReceiverFinishRejectsTruncationAndHashMismatchWithoutCleanup(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"truncated", []byte("ab")}, {"hash mismatch", []byte("abx")},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := isolateReceiver(t)
			file := receiverFile(t, root)
			receiver, err := NewReceiver(receiverItem([]byte("abc")), file)
			if err != nil {
				t.Fatal(err)
			}
			if err := receiver.WriteAt(test.data, 0); err != nil {
				t.Fatal(err)
			}
			err = receiver.Finish()
			if err == nil {
				t.Fatal("accepted an incomplete or wrong stream")
			}
			if test.name == "hash mismatch" {
				var mismatch *MismatchError
				if !errors.As(err, &mismatch) || mismatch.What != "SHA-256" {
					t.Fatalf("canonical mismatch cause lost: %v", err)
				}
			}
			if err := receiver.WriteAt([]byte("c"), receiver.Received()); err == nil {
				t.Fatal("finished failure resumed")
			}
			if !bytes.Equal(receiverBytes(t, file), test.data) {
				t.Fatal("verification changed the caller's bytes")
			}
			if _, err := file.Stat(); err != nil {
				t.Fatalf("verification closed the caller's descriptor: %v", err)
			}
		})
	}
}

func TestReceiverFinishUsesCanonicalToolsMerkleVerifier(t *testing.T) {
	for _, test := range []string{"valid", "root", "stored tree"} {
		t.Run(test, func(t *testing.T) {
			root := isolateReceiver(t)
			file := receiverFile(t, root)
			item, data := verityTools(t)
			if test == "root" {
				item.RootHash = strings.Repeat("f", 64)
			}
			if test == "stored tree" {
				data[8192] ^= 1
				sum := sha256.Sum256(data)
				item.SHA256 = hex.EncodeToString(sum[:])
			}
			receiver, err := NewReceiver(item, file)
			if err != nil {
				t.Fatal(err)
			}
			receiverWriteAll(t, receiver, data)
			err = receiver.Finish()
			if (err == nil) != (test == "valid") {
				t.Fatalf("canonical tools verification = %v", err)
			}
			if test == "root" && !strings.Contains(err.Error(), "root_hash") {
				t.Fatalf("tools root was not checked: %v", err)
			}
			if test == "stored tree" && !strings.Contains(err.Error(), "verity tree") {
				t.Fatalf("stored Merkle tree was not checked: %v", err)
			}
			if !bytes.Equal(receiverBytes(t, file), data) {
				t.Fatal("tools verification altered or removed bytes")
			}
		})
	}
}

func TestReceiverConstructorRefusesInvalidMetadataWithoutTakingDescriptor(t *testing.T) {
	for _, alter := range []func(*Item){
		func(item *Item) { item.SHA256 = "" }, func(item *Item) { item.Size = 0 }, func(item *Item) { item.Arch = "unknown" },
		func(item *Item) { item.URL = "http://fixture.invalid/input" }, func(item *Item) { item.Kind = KindImage },
	} {
		root := isolateReceiver(t)
		file := receiverFile(t, root)
		item := receiverItem([]byte("abc"))
		alter(&item)
		if receiver, err := NewReceiver(item, file); err == nil || receiver != nil {
			t.Fatal("accepted invalid transfer metadata")
		}
		if _, err := file.WriteAt([]byte("owned"), 0); err != nil {
			t.Fatalf("constructor took descriptor ownership: %v", err)
		}
		if !bytes.Equal(receiverBytes(t, file), []byte("owned")) {
			t.Fatal("constructor removed the caller's file")
		}
	}
	if receiver, err := NewReceiver(receiverItem([]byte("abc")), nil); err == nil || receiver != nil {
		t.Fatal("accepted a nil descriptor")
	}
}

func TestReceiverConstructorPreservesNonemptyAndLinkedForeignFiles(t *testing.T) {
	for _, test := range []string{"nonempty", "linked"} {
		t.Run(test, func(t *testing.T) {
			root := isolateReceiver(t)
			file := receiverFile(t, root)
			if test == "nonempty" {
				if _, err := file.WriteAt([]byte("foreign"), 0); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Link(file.Name(), filepath.Join(root, "foreign-link")); err != nil {
					t.Fatal(err)
				}
			}
			before := receiverBytes(t, file)
			info, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if receiver, err := NewReceiver(receiverItem([]byte("abc")), file); err == nil || receiver != nil {
				t.Fatal("accepted a foreign nonempty or linked file")
			}
			after, err := os.Stat(file.Name())
			if err != nil || !os.SameFile(info, after) || !bytes.Equal(receiverBytes(t, file), before) {
				t.Fatalf("constructor changed the foreign file: %v", err)
			}
			if _, err := file.WriteAt([]byte("kept"), 0); err != nil {
				t.Fatalf("constructor closed the foreign descriptor: %v", err)
			}
		})
	}
}

func TestReceiverRejectsPipeWithoutClosingEitherEnd(t *testing.T) {
	isolateReceiver(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	if receiver, err := NewReceiver(receiverItem([]byte("abc")), writer); err == nil || receiver != nil {
		t.Fatal("accepted a nonregular descriptor")
	}
	if _, err := writer.Write([]byte("kept")); err != nil {
		t.Fatalf("receiver closed the pipe: %v", err)
	}
	data := make([]byte, 4)
	if _, err := io.ReadFull(reader, data); err != nil || string(data) != "kept" {
		t.Fatalf("foreign pipe changed: %q, %v", data, err)
	}
}

func TestReceiverWriteErrorStopsWithoutClosingOrRemoving(t *testing.T) {
	root := isolateReceiver(t)
	file := receiverFile(t, root)
	readonly, err := os.Open(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readonly.Close() })
	receiver, err := NewReceiver(receiverItem([]byte("abc")), readonly)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteAt([]byte("abc"), 0); err == nil {
		t.Fatal("read-only write succeeded")
	}
	if receiver.Received() != 0 || len(receiverBytes(t, file)) != 0 {
		t.Fatal("failed write advanced or changed bytes")
	}
	if err := receiver.Finish(); err == nil {
		t.Fatal("write failure was forgotten")
	}
	if _, err := readonly.Stat(); err != nil {
		t.Fatalf("receiver closed a failed descriptor: %v", err)
	}
	if _, err := file.WriteAt([]byte("foreign"), 0); err != nil {
		t.Fatalf("receiver closed another handle: %v", err)
	}
}

func TestReceiverDoesNotImposeAnAdditionalFileSizeCap(t *testing.T) {
	root := isolateReceiver(t)
	file := receiverFile(t, root)
	item := receiverItem([]byte("x"))
	item.Size = 5 << 30
	receiver, err := NewReceiver(item, file)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteAt([]byte("x"), 0); err != nil || receiver.Received() != 1 {
		t.Fatalf("large signed size cannot start: %v", err)
	}
	if err := receiver.Finish(); err == nil {
		t.Fatal("partial large stream was claimed complete")
	}
}

func TestReceiverChunkLimitRejectsAnOtherwiseExactStream(t *testing.T) {
	root := isolateReceiver(t)
	file := receiverFile(t, root)
	data := bytes.Repeat([]byte("x"), TransferChunkSize+1)
	receiver, err := NewReceiver(receiverItem(data), file)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteAt(data, 0); err == nil {
		t.Fatal("accepted one chunk over the transfer limit")
	}
	if receiver.Received() != 0 || len(receiverBytes(t, file)) != 0 {
		t.Fatal("oversized chunk wrote any bytes")
	}
	if err := receiver.WriteAt(data[:TransferChunkSize], 0); err == nil {
		t.Fatal("failed chunk-limit transfer resumed")
	}
}

func TestReceiverRefusesAnOtherwiseValidOCIImage(t *testing.T) {
	root := isolateReceiver(t)
	file := receiverFile(t, root)
	item := Item{Kind: KindImage, Name: "image", Arch: "amd64", Digest: "sha256:" + strings.Repeat("a", 64), RootHash: strings.Repeat("b", 64), DataBlocks: 1}
	if err := item.validate(); err != nil {
		t.Fatalf("invalid fixture: %v", err)
	}
	if receiver, err := NewReceiver(item, file); err == nil || receiver != nil {
		t.Fatal("OCI metadata has no byte stream SHA-256 or size")
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("image refusal closed its descriptor: %v", err)
	}
}

func TestReceiverCanonicalFinishRefusesExternalAppend(t *testing.T) {
	root := isolateReceiver(t)
	file := receiverFile(t, root)
	data := []byte("abc")
	receiver, err := NewReceiver(receiverItem(data), file)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("foreign"), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	before := receiverBytes(t, file)
	err = receiver.Finish()
	var mismatch *MismatchError
	if !errors.As(err, &mismatch) || mismatch.What != "size" {
		t.Fatalf("canonical descriptor size check missing: %v", err)
	}
	if !bytes.Equal(receiverBytes(t, file), before) {
		t.Fatal("receiver truncated foreign appended bytes")
	}
}

func TestReceiverNeverReopensAReplacedDescriptorPath(t *testing.T) {
	root := isolateReceiver(t)
	file := receiverFile(t, root)
	receiver, err := NewReceiver(receiverItem([]byte("abcd")), file)
	if err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteAt([]byte("ab"), 0); err != nil {
		t.Fatal(err)
	}
	name := file.Name()
	saved := name + "-saved"
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(name, saved); err != nil {
		t.Fatal(err)
	}
	foreign, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foreign.Close() })
	if _, err := foreign.WriteAt([]byte("ab"), 0); err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteAt([]byte("cd"), 2); err == nil {
		t.Fatal("receiver reopened a foreign replacement path")
	}
	if got := receiverBytes(t, foreign); string(got) != "ab" {
		t.Fatalf("foreign replacement changed: %q", got)
	}
	if got, err := os.ReadFile(saved); err != nil || string(got) != "ab" {
		t.Fatalf("original file changed: %q, %v", got, err)
	}
	if _, err := foreign.WriteAt([]byte("kept"), 0); err != nil {
		t.Fatalf("receiver closed the foreign descriptor: %v", err)
	}
}
