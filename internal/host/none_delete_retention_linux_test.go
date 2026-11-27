//go:build linux

package host

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

func TestNoneDirectoryMutationRetainsDeleteBeforeMetadataCapture(t *testing.T) {
	directory := &noneDirectoryCleanupV1{started: true, complete: true}
	directory.self = directory
	source := &noneDeleteSourceV1{directory: directory}
	source.self = source
	if err := source.Close(); err == nil {
		t.Fatal("completed runtime unlink discarded its original transaction before metadata capture")
	}
	if directory.closed || source.directory != directory {
		t.Fatal("metadata preparation failure closed the original directory owner")
	}
}

func TestNoneDirectoryMutationRetainsDeleteAfterIncompleteCaptureCloses(t *testing.T) {
	directory := &noneDirectoryCleanupV1{started: true, complete: true}
	directory.self = directory
	metadata := &noneMetadataCleanupV1{}
	metadata.self = metadata
	source := &noneDeleteSourceV1{directory: directory, metadata: metadata}
	source.self = source
	if err := source.Close(); err == nil {
		t.Fatal("closed preparation released the still unfinished original deletion")
	}
	if !metadata.closed || source.metadata != nil || directory.closed {
		t.Fatal("the original delete did not retain its directory while retiring only metadata preparation")
	}
}

func TestNoneIncompleteDirectoryCaptureCannotRemove(t *testing.T) {
	tx := &noneDirectoryCleanupV1{}
	tx.self = tx
	if err := tx.Remove(context.Background()); err == nil || tx.started {
		t.Fatal("an incomplete actual directory capture admitted removal")
	}
}

func TestNoneDirectoryCloseRetainsOriginalUnconfirmedRoot(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	supported := supply.CheckFileCloseThreadV1() == nil
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.OpenRoot(t.TempDir())
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	defer parent.Close()
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	tx := &noneDirectoryCleanupV1{root: root, parent: parent}
	tx.self = tx
	if err := tx.Close(); err == nil || tx.closed || tx.root != root || tx.parent != parent {
		t.Fatal("an unconfirmed directory root retired its original transaction")
	}
	if _, err := parent.Stat("."); supported == (err == nil) {
		t.Fatal("directory parent retirement did not match its actual supported close profile", err)
	}
	first := tx.retirement.roots[root]
	if first == nil || first.root != root {
		t.Fatal("the directory has no retained same-object first native close record")
	}
	if err := tx.Close(); err == nil || tx.closed || tx.retirement.roots[root] != first {
		t.Fatal("directory retry inferred native retirement from a repeated nil Close")
	}
}
