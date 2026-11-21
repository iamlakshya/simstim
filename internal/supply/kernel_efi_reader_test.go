package supply

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type countedKernelReaderV1 struct {
	reader       io.ReaderAt
	bytes, calls int
	after        func()
}

func (reader *countedKernelReaderV1) ReadAt(data []byte, offset int64) (int, error) {
	reader.calls++
	reader.bytes += len(data)
	n, err := reader.reader.ReadAt(data, offset)
	if reader.after != nil {
		reader.after()
	}
	return n, err
}

func TestEFIHeaderReaderIsBoundedAndCancellationPreservesTheSameReaderV1(t *testing.T) {
	data := kernelEFIFormatFixtureV1("amd64")
	// A hostile COFF debug table must not trigger symbol-table allocation.
	binary.LittleEndian.PutUint32(data[0x84+8:], ^uint32(0))
	binary.LittleEndian.PutUint32(data[0x84+12:], ^uint32(0))
	reader := &countedKernelReaderV1{reader: bytes.NewReader(data)}
	if err := checkKernelEFIReaderV1(t.Context(), "amd64", reader, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if reader.bytes > 1024 || reader.calls != 4 {
		t.Fatal("EFI format check read more than its bounded headers", reader.bytes, reader.calls)
	}
	ctx, cancel := context.WithCancel(t.Context())
	reader = &countedKernelReaderV1{reader: bytes.NewReader(data), after: cancel}
	if err := checkKernelEFIReaderV1(ctx, "amd64", reader, int64(len(data))); !errors.Is(err, context.Canceled) || reader.calls != 1 {
		t.Fatal("header read ignored original cancellation", err, reader.calls)
	}
	wrong := append([]byte(nil), data...)
	binary.LittleEndian.PutUint16(wrong[0x84+2:], ^uint16(0))
	if err := CheckKernelEFI("amd64", wrong); err == nil {
		t.Fatal("unbounded section count accepted")
	}
}

func TestCanonicalEFIBytesAndOpenDescriptorChecksRequireFormatV1(t *testing.T) {
	for _, valid := range []bool{true, false} {
		data := kernelEFIFormatFixtureV1("amd64")
		if !valid {
			data[0x202] = 0
		}
		base := ReleaseFolder("1.2.3") + "/"
		item := NewItem(KindKernel, KernelEFIName, "6.18.63", "amd64", data, base+KernelEFIAsset("amd64"))
		item.Licence, item.SourceURL, item.Released = "GPL-2.0-only", base+KernelSourceAsset(item.Version), "2026-11-01"
		if err := Check(item, item.Name, data); (err == nil) != valid {
			t.Fatal("canonical byte check ignored actual EFI role format", err)
		}
		path := filepath.Join(t.TempDir(), "original-kernel")
		if err := os.WriteFile(path, data, 0o444); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckOpenContext(t.Context(), item, file); (err == nil) != valid {
			file.Close()
			t.Fatal("descriptor check ignored actual EFI role format", err)
		}
		if valid {
			if _, err := file.Seek(333, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			if err := checkKernelEFIItemV1(t.Context(), item, file); err != nil {
				t.Fatal(err)
			}
			position, err := file.Seek(0, io.SeekCurrent)
			if err != nil || position != 333 {
				t.Fatal("EFI header parser changed its borrowed OFD offset", position, err)
			}
		}
		file.Close()
	}
}
