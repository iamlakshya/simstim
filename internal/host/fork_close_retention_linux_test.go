//go:build linux

package host

import (
	"os"
	"testing"
)

func TestForkCloseCannotDeleteOrSkipChildAfterOperationFailureV1(t *testing.T) {
	parent, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	const id = "a2a2a2a2"
	if err := parent.Mkdir(id, 0700); err != nil {
		t.Fatal(err)
	}
	created, err := parent.Lstat(id)
	if err != nil {
		t.Fatal(err)
	}
	root, err := parent.OpenRoot(id)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := root.OpenFile("retained-child-data", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	// These unissued operations must refuse cleanup, never provide native
	// closure or permission to remove the real temporary reservation below.
	operation, outer := &SessionOperation{}, &SessionOperation{}
	outer.active.Store(true)
	child := &forkChildV1{operation: operation, reservation: &sessionReservation{id: id, parent: parent, dir: root, created: created}}
	source := &ForkSourceV1{operation: outer, children: []*forkChildV1{child}}
	first := source.Close()
	if first == nil {
		t.Fatal("an operation without native closure authorised child cleanup")
	}
	for range 2 {
		again := source.Close()
		if again == nil || again.Error() != first.Error() {
			t.Fatal("retry discarded the original child close error", first, again)
		}
		if source.closed || child.closed || child.operation != operation || child.reservation.dir != root || !outer.active.Load() {
			t.Fatal("an unresolved actual child released its parent operation or custody")
		}
		if _, err := root.Stat("retained-child-data"); err != nil {
			t.Fatal("failed native operation cleanup deleted child state", err)
		}
	}
}

func TestForkLaunchedReservationCloseErrorStaysFailureV1(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	// Resource-only prefix: an already-closed actual root cannot supply the
	// required first native close fact, even on a later retry.
	child := &forkChildV1{reservation: &sessionReservation{dir: root, launched: true}}
	first := child.closeResourcesV1()
	if first == nil {
		t.Fatal("a preclosed reservation supplied native retirement")
	}
	for range 2 {
		again := child.closeResourcesV1()
		if again == nil || again.Error() != first.Error() || child.closed || child.reservation.dir != root {
			t.Fatal("a retry replaced the original failed reservation", first, again)
		}
	}
}
