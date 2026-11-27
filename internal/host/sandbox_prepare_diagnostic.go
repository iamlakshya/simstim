package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This validates report DATA only. Its caller independently owns the actual
// native cursor, sender, driver cleanup acknowledgement and creation join.
func decodeSandboxPrepareCleanupDiagnosticV1(raw []byte) (error, error) {
	var result proto.SandboxPrepareCleanupResultV1
	if len(raw) == 0 || len(raw) > proto.SandboxPrepareCleanupResultMaxV1 || !api.ExactJSONFields(raw, result) || json.Unmarshal(raw, &result) != nil || result.Version != 1 {
		return nil, sessionLeaseError("the original fixed-test cleanup returned an invalid diagnostic result")
	}
	fields, err := api.JSONObjectFields(raw, "v", "diagnostic")
	if err != nil || len(fields) != 2 || len(fields["v"]) == 0 || len(fields["diagnostic"]) == 0 {
		return nil, errors.Join(sessionLeaseError("the fixed-test cleanup diagnostic omitted its closed fields"), err)
	}
	if result.Diagnostic == nil {
		if !bytes.Equal(bytes.TrimSpace(fields["diagnostic"]), []byte("null")) {
			return nil, sessionLeaseError("the fixed-test cleanup diagnostic has another null representation")
		}
		return nil, nil
	}
	diagnostic := result.Diagnostic
	if diagnostic.Code != "sandbox_cleanup_diagnostic" || diagnostic.Message == "" || len(diagnostic.Message) > proto.SandboxPrepareCleanupDiagnosticMaxV1 || !utf8.ValidString(diagnostic.Message) || diagnostic.Hint == "" || len(diagnostic.Hint) > 256 || len(diagnostic.Data) != 0 {
		return nil, sessionLeaseError("the fixed-test cleanup diagnostic has another closed error shape")
	}
	return diagnostic, nil
}
