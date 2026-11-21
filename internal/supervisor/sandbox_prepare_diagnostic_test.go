package supervisor

import (
	"errors"
	"testing"
)

type prepareDiagnosticRuntimeV2 struct {
	prepareCleanupRuntimeV1
	diagnostic error
}

func (runtime *prepareDiagnosticRuntimeV2) PreparedCloseDiagnosticsV2() error {
	return runtime.diagnostic
}

func TestSandboxPrepareRetiredIOFailureSurvivesCleanupAndSecondClose(t *testing.T) {
	first := errors.New("close original preparation disk: I/O error")
	runtime := &prepareDiagnosticRuntimeV2{prepareCleanupRuntimeV1: prepareCleanupRuntimeV1{release: make(chan struct{}), entered: make(chan struct{})}, diagnostic: first}
	close(runtime.release)
	s := &Supervisor{}
	if cleanup := s.closeSandboxPrepareRuntimeV1(runtime, true); cleanup != nil {
		t.Fatal("retained I/O report became unknown physical cleanup", cleanup)
	}
	if !errors.Is(s.prepareCleanupDiagnostic, first) {
		t.Fatal("positive destruction hid the original I/O failure", s.prepareCleanupDiagnostic)
	}
	if cleanup := s.closeSandboxPrepareRuntimeV1(runtime, true); cleanup != nil || !errors.Is(s.prepareCleanupDiagnostic, first) || runtime.destroy.Load() != 1 {
		t.Fatal("second close replaced the original outcome or repeated Destroy", cleanup, s.prepareCleanupDiagnostic)
	}
}

func TestSandboxPrepareDiagnosticAdaptersKeepActualRuntime(t *testing.T) {
	first := errors.New("original runtime first-close error")
	actual := &diagnosticWallRuntimeV2{diagnostic: first}
	for _, runtime := range []any{vmRuntime{Sandbox: actual}, processSandbox{Sandbox: actual}, &failedSandboxPreparationV1{runtime: actual}} {
		if diagnostic := sandboxPrepareCloseDiagnosticsV2(runtime); diagnostic != first {
			t.Fatal("adapter hid or reconstructed the actual runtime diagnostic", diagnostic)
		}
	}
}

type diagnosticWallRuntimeV2 struct {
	vmRuntimeFixture
	diagnostic error
}

func (runtime *diagnosticWallRuntimeV2) PreparedCloseDiagnosticsV2() error {
	return runtime.diagnostic
}
