//go:build linux

package microvm

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestActualPhaseCloseDiagnosticSurvivesRetiredReportCustodyV4(t *testing.T) {
	path := os.Getenv("VM_REAL_LATE_CLOSE_FILE")
	if path == "" {
		t.Skip("Root must admit the real native late-close filesystem fixture")
	}
	unfilteredRetirementFixtureV2(t)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatal("the admitted native fixture path is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := &microVMFileRetirementV2{file: file}
	first := owner.closeV2() // Actual unborrowed local file, not a VM cleanup grant.
	if first == nil || !owner.retired || errors.Is(first, os.ErrClosed) {
		t.Fatal("the admitted fixture did not produce an actual retired close diagnostic", first)
	}
	phase := &vmmExecPhaseV1{ctx: t.Context(), readerRetirement: owner}
	lease := &bootRuntimeLeaseV1{phases: []*vmmExecPhaseV1{phase}}
	lease.mu.Lock()
	lease.capturePhaseDiagnosticsV4()
	lease.phases = nil // Report-only custody fixture; no native VM source is issued.
	lease.mu.Unlock()
	sandbox := &sandbox{agentsLease: lease}
	if err := sandbox.releaseAgentsLease(); err != nil {
		t.Fatal(err)
	}
	if sandbox.agentsLease != nil || !errors.Is(sandbox.PreparedCloseDiagnosticsV2(), first) {
		t.Fatal("successful resource retirement erased the actual first I/O outcome")
	}
	if err := sandbox.releaseAgentsLease(); err != nil || !errors.Is(sandbox.PreparedCloseDiagnosticsV2(), first) {
		t.Fatal("later cleanup changed the bounded original report", err)
	}
}

func TestUnknownPhaseCloseReportCannotAcknowledgeNativeCleanupV4(t *testing.T) {
	unfilteredRetirementFixtureV2(t)
	file, err := os.Create(filepath.Join(t.TempDir(), "already-closed"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	owner := &microVMFileRetirementV2{file: file}
	if err := owner.closeV2(); err == nil || owner.retired {
		t.Fatal("already-closed became a native retirement proof", err)
	}
	phase := &vmmExecPhaseV1{ctx: t.Context(), readerRetirement: owner}
	lease := &bootRuntimeLeaseV1{phases: []*vmmExecPhaseV1{phase}}
	sandbox := &sandbox{agentsLease: lease}
	for range 2 {
		if err := sandbox.releaseAgentsLease(); err == nil {
			t.Fatal("a report-only phase without an actual native source acknowledged cleanup")
		}
		if sandbox.agentsLease != lease || len(lease.phases) != 1 || lease.phases[0] != phase || !errors.Is(sandbox.PreparedCloseDiagnosticsV2(), os.ErrClosed) {
			t.Fatal("failed cleanup lost its original objects or diagnostic")
		}
	}
}
