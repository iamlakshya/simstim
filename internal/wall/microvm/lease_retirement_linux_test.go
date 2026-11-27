//go:build linux

package microvm

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
)

func requireRetirementThreadV2(t *testing.T) {
	t.Helper()
	if err := actualUnfilteredCloseThreadV2(); err != nil {
		t.Skip("needs the Root-admitted unfiltered Linux close fixture: ", err)
	}
}

func TestVMLeaseRetirementNeverClosesReusedFD(t *testing.T) {
	requireRetirementThreadV2(t)
	lease, err := os.CreateTemp(t.TempDir(), "owned-lease")
	if err != nil {
		t.Fatal(err)
	}
	number := lease.Fd()
	owner := &sandbox{lease: lease}
	if err := owner.releaseLease(); err != nil {
		t.Fatal(err)
	}
	if owner.lease != nil {
		t.Fatal("retired actual lease retained as an unclosed borrower")
	}
	foreign, err := os.CreateTemp(t.TempDir(), "foreign")
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if foreign.Fd() != number {
		t.Skip("the admitted fixture did not reuse its just-retired descriptor")
	}
	if err := owner.releaseLease(); err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.Write([]byte("still owned by its actual creator")); err != nil {
		t.Fatal("second cleanup touched a reused foreign descriptor", err)
	}
}

func TestVMAlreadyClosedLeaseRemainsUnknown(t *testing.T) {
	requireRetirementThreadV2(t)
	lease, err := os.CreateTemp(t.TempDir(), "already-closed")
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	owner := &sandbox{lease: lease}
	if err := owner.releaseLease(); err == nil {
		t.Fatal("pre-existing closed object became a retirement proof")
	}
	if owner.lease != lease {
		t.Fatal("unknown actual lease owner was discarded")
	}
	if err := owner.releaseLease(); err == nil {
		t.Fatal("unknown first close became success on retry")
	}
}

func TestVMMetadataRetirementPreservesActualWriteFailure(t *testing.T) {
	requireRetirementThreadV2(t)
	path := t.TempDir() + "/metadata"
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	actual, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := actual.Write([]byte("forbidden write"))
	if writeErr == nil {
		t.Fatal("read-only native writer unexpectedly succeeded")
	}
	owner := &sandbox{preparationErr: writeErr}
	if err := closeVMPreparationFileV1(owner, actual); err != nil {
		t.Fatal(err)
	}
	if err := owner.ReleasePreparedV1(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(owner.preparationErr, syscall.EBADF) {
		t.Fatal("actual original I/O failure was erased", owner.preparationErr)
	}
	if _, err := owner.Start(context.Background()); err == nil {
		t.Fatal("a cleaned failed factory started again")
	}
}

func TestVMReleasedFactoryCannotReopenDestructiveCustody(t *testing.T) {
	owner := &sandbox{preparedReleased: true}
	if err := owner.Destroy(); err == nil {
		t.Fatal("released metadata was reopened into destructive authority")
	}
}
