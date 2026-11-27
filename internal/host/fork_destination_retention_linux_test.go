//go:build linux

package host

import (
	"context"
	"os"
	"testing"

	"github.com/iamlakshya/simstim/internal/supply"
)

// These partials exercise resource retirement only. No test constructs an
// accepted native destination from a file, root or copied operation.
func TestPartialForkDestinationCannotGrantWritesV1(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFile("actual-partial-loan", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	destination := &ForkDestinationV1{pendingRoot: root, pendingFile: file}
	destination.issuer = destination
	called := false
	if err := destination.WithRootV1(context.Background(), func(*os.Root) error { called = true; return nil }); err == nil || called {
		t.Fatal("a cleanup-only received partial granted a destination writer")
	}
	profile := supply.CheckFileCloseThreadV1()
	first := destination.Close()
	if profile == nil && (first != nil || !destination.closed) {
		t.Fatal("the original partial resources did not retire", first)
	}
	if profile != nil && (first == nil || destination.closed) {
		t.Fatal("unknown native close profile claimed retirement")
	}
	again := destination.Close()
	if (first == nil) != (again == nil) || first != nil && first.Error() != again.Error() {
		t.Fatal("retry changed original close facts", first, again)
	}
	if destination.accepted || destination.pendingFile != file || destination.pendingRoot != root {
		t.Fatal("resource cleanup replaced the original partial or issued a destination")
	}
}

func TestForkDestinationRetainsFirstCloseFailureV1(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "closed-before-owner-")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	destination := &ForkDestinationV1{pendingFile: file}
	destination.issuer = destination
	first := destination.Close()
	if first == nil {
		t.Fatal("a preclosed object supplied first-close proof")
	}
	for range 2 {
		again := destination.Close()
		if again == nil || again.Error() != first.Error() || destination.closed || destination.pendingFile != file {
			t.Fatal("a retry lost the actual failed partial", first, again)
		}
	}
}

func TestForkDestinationRetainsFailedEmptinessReaderV1(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := root.OpenFile("unexpected-child-data", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	// This unaccepted resource-only prefix cannot pass WithRootV1.
	destination := &ForkDestinationV1{operation: &SessionOperation{native: &sessionFileLease{root: root}}}
	if err := destination.emptyV1(); err == nil {
		t.Fatal("a populated reservation passed the emptiness check")
	}
	if destination.probe == nil || destination.accepted {
		t.Fatal("the failed original reader was dropped or accepted")
	}
	if supply.CheckFileCloseThreadV1() == nil && destination.retirement.checkFilesV4([]*os.File{destination.probe}) != nil {
		t.Fatal("the failed reader lost its actual first native close facts")
	}
}
