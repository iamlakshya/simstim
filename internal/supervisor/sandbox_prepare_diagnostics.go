package supervisor

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/proto"
)

// Concrete adapters forward only the original runtime's retained diagnostic.
// These observations cannot certify retirement or replace Destroy.
func sandboxPrepareCloseDiagnosticsV2(runtime any) error {
	if actual, ok := runtime.(interface{ PreparedCloseDiagnosticsV2() error }); ok {
		return actual.PreparedCloseDiagnosticsV2()
	}
	return nil
}

// PreparedCloseDiagnosticsV2 preserves the original VM's first I/O outcome.
func (runtime vmRuntime) PreparedCloseDiagnosticsV2() error {
	return sandboxPrepareCloseDiagnosticsV2(runtime.Sandbox)
}

// PreparedCloseDiagnosticsV2 preserves a partial constructor's I/O outcome.
func (runtime *failedSandboxPreparationV1) PreparedCloseDiagnosticsV2() error {
	return sandboxPrepareCloseDiagnosticsV2(runtime.runtime)
}

// PreparedCloseDiagnosticsV2 preserves the original process runtime's outcome.
func (runtime processSandbox) PreparedCloseDiagnosticsV2() error {
	return sandboxPrepareCloseDiagnosticsV2(runtime.Sandbox)
}

func (s *Supervisor) sandboxPrepareCleanupReplyV1(request uint64) (proto.Response, error) {
	if request != 4 || !s.isSandboxPrepareBootV1() {
		return proto.Response{}, execRefusal("cleanup diagnostics have no original fixed-test cancellation")
	}
	s.mu.Lock()
	diagnostic := s.prepareCleanupDiagnostic
	s.mu.Unlock()
	result := proto.SandboxPrepareCleanupResultV1{Version: 1}
	if diagnostic != nil {
		text := strings.ToValidUTF8(diagnostic.Error(), "\uFFFD")
		if text == "" {
			text = "the original runtime reported an I/O error during cleanup"
		}
		if len(text) > proto.SandboxPrepareCleanupDiagnosticMaxV1 {
			text = text[:proto.SandboxPrepareCleanupDiagnosticMaxV1]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
		}
		result.Diagnostic = &proto.Error{Code: "sandbox_cleanup_diagnostic", Message: text, Hint: "Run host doctor and check storage before retrying the fixed test."}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return proto.Response{}, err
	}
	if len(raw) > proto.SandboxPrepareCleanupResultMaxV1 {
		return proto.Response{}, execRefusal("the fixed-test cleanup diagnostic exceeds its closed result bound")
	}
	return proto.Response{ID: request, OK: true, Result: raw}, nil
}
