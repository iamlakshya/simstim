package host

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestSandboxPrepareCleanupDiagnosticKeepsFailureSeparateFromDecode(t *testing.T) {
	raw, err := json.Marshal(proto.SandboxPrepareCleanupResultV1{Version: 1, Diagnostic: &proto.Error{Code: "sandbox_cleanup_diagnostic", Message: "close original disk: I/O error", Hint: "Check storage."}})
	if err != nil {
		t.Fatal(err)
	}
	diagnostic, invalid := decodeSandboxPrepareCleanupDiagnosticV1(raw)
	if invalid != nil || diagnostic == nil || !strings.Contains(diagnostic.Error(), "I/O error") {
		t.Fatal("actual close diagnostic was lost or became malformed cleanup", diagnostic, invalid)
	}
	if diagnostic, invalid := decodeSandboxPrepareCleanupDiagnosticV1([]byte(`{"v":1,"diagnostic":null}`)); diagnostic != nil || invalid != nil {
		t.Fatal("clean fixed-test report was rejected", diagnostic, invalid)
	}
}

func TestSandboxPrepareCleanupDiagnosticRejectsMalformedResult(t *testing.T) {
	for _, raw := range []string{
		``, `null`, `{}`, `{"v":1}`, `{"v":1,"diagnostic":null,"ready":true}`,
		`{"v":2,"diagnostic":null}`, `{"v":1,"v":1,"diagnostic":null}`,
		`{"v":1,"Diagnostic":null}`, `{"v":1,"diagnostic":{}}`,
		`{"v":1,"diagnostic":{"code":"other","message":"I/O error","hint":"Retry."}}`,
		`{"v":1,"diagnostic":{"code":"sandbox_cleanup_diagnostic","message":"I/O error","hint":""}}`,
		`{"v":1,"diagnostic":{"code":"sandbox_cleanup_diagnostic","message":"I/O error","hint":"Retry.","data":{"retired":true}}}`,
		`{"v":1,"diagnostic":{"code":"sandbox_cleanup_diagnostic","message":"first","message":"second","hint":"Retry."}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if diagnostic, invalid := decodeSandboxPrepareCleanupDiagnosticV1([]byte(raw)); invalid == nil || diagnostic != nil {
				t.Fatal("invalid result became cleanup report", diagnostic, invalid)
			}
		})
	}
	large, err := json.Marshal(proto.SandboxPrepareCleanupResultV1{Version: 1, Diagnostic: &proto.Error{Code: "sandbox_cleanup_diagnostic", Message: strings.Repeat("x", proto.SandboxPrepareCleanupDiagnosticMaxV1+1), Hint: "Retry."}})
	if err != nil {
		t.Fatal(err)
	}
	if diagnostic, invalid := decodeSandboxPrepareCleanupDiagnosticV1(large); invalid == nil || diagnostic != nil {
		t.Fatal("oversized diagnostic was accepted", diagnostic, invalid)
	}
}
