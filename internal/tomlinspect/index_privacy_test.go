//go:build darwin || linux

package tomlinspect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func TestOwnedMetadataContainsOnlyReferencesNotSourceValues(t *testing.T) {
	factory := fixtureIndex(t)
	marker := []byte("private-command-marker-never-copy-this-value-into-the-metadata-index")
	var input strings.Builder
	fmt.Fprintf(&input, "command=%q\n", marker)
	for index := range 4000 {
		fmt.Fprintf(&input, "field_%06d=%d\n", index, index)
	}
	checked := false
	factory.onClose = func(file *os.File, slab []byte) error {
		if bytes.Contains(slab, marker) {
			return errors.New("source value appeared in metadata workspace")
		}
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if info.Size() == 0 {
			return errors.New("privacy test did not exercise backing-file eviction")
		}
		var buffer [4096 + 128]byte
		carry := 0
		for offset := int64(0); offset < info.Size(); {
			n, err := file.ReadAt(buffer[carry:carry+4096], offset)
			if err != nil && err != io.EOF {
				return err
			}
			if n == 0 {
				return io.ErrNoProgress
			}
			if bytes.Contains(buffer[:carry+n], marker) {
				return errors.New("source value appeared in metadata backing bytes")
			}
			total := carry + n
			carry = min(len(marker)-1, total)
			copy(buffer[:carry], buffer[total-carry:total])
			offset += int64(n)
		}
		checked = true
		return nil
	}
	inspector, err := NewInspector(factory)
	if err != nil {
		t.Fatal(err)
	}
	text := input.String()
	if err := inspector.Inspect(t.Context(), strings.NewReader(text), int64(len(text)), syntaxOnlyVisitor{}); err != nil {
		t.Fatal(err)
	}
	if !checked || factory.opened != factory.closed {
		t.Fatal("privacy inspection did not close its owned index")
	}
}

type cancelAfterVisitor struct{ cancel context.CancelFunc }

func (v cancelAfterVisitor) EnterTable(PathCursor, TableCursor) error { return nil }
func (cancelAfterVisitor) LeaveTable(PathCursor) error                { return nil }
func (cancelAfterVisitor) EnterArray(PathCursor, ArrayCursor) error   { return ErrSyntax }
func (cancelAfterVisitor) LeaveArray(PathCursor) error                { return ErrSyntax }
func (cancelAfterVisitor) Scalar(PathCursor, ScalarCursor) error      { return ErrSyntax }
func (v cancelAfterVisitor) Finish() error                            { v.cancel(); return nil }

func TestFinalCancellationAndVisitorFailureCloseIndex(t *testing.T) {
	factory := fixtureIndex(t)
	inspector, err := NewInspector(factory)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	input := "v=1\n"
	err = inspector.Inspect(ctx, strings.NewReader(input), int64(len(input)), cancelAfterVisitor{cancel: cancel})
	if !errors.Is(err, context.Canceled) || factory.opened != factory.closed {
		t.Fatalf("final cancellation granted or retained a lease: %v", err)
	}
	if err := inspector.Inspect(t.Context(), strings.NewReader(input), int64(len(input)), syntaxOnlyVisitor{}); err != nil {
		t.Fatal("fresh serial lease did not reset state:", err)
	}
}
