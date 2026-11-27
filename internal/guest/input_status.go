package guest

import (
	"encoding/json"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
)

// inputStatus reads the SAME Init operation. It grants no authority to the
// host, which must retain its own canonical sender-EOF/current-run receipt.
func (g *Init) inputStatus(raw []byte) (inputtransfer.StatusResult, error) {
	var scope inputtransfer.Scope
	if !api.ExactJSONFields(raw, scope) || json.Unmarshal(raw, &scope) != nil || !api.ValidSessionID(scope.Session) || !scope.Nonce.Valid() || (api.InputSummary{Version: api.SessionInputsV1, Nonce: scope.Nonce, Digest: scope.Digest}).Validate(api.DefaultCopyMax, api.DefaultSeedMax) != nil {
		return inputtransfer.StatusResult{}, inputError("invalid input completion scope")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	op := g.inputs
	if g.ended {
		return inputtransfer.StatusResult{}, inputError("the input owner ended")
	}
	if op == nil {
		return inputtransfer.StatusResult{Version: 1, Scope: scope, Joined: true}, nil
	}
	if !op.late || op.control.Scope != scope {
		return inputtransfer.StatusResult{}, inputError("no matching late input operation is owned")
	}
	joined := !inputPhaseActive(op)
	return inputtransfer.StatusResult{Version: 1, Scope: scope, Found: true, Joined: joined, Complete: joined && op.success}, nil
}
