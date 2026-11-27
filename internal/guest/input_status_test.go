package guest

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/inputtransfer"
)

func TestInputStatusOnlyReportsExactJoinedExistingLateOperation(t *testing.T) {
	scope := inputtransfer.Scope{Session: "primary234", Nonce: api.InputNonce(strings.Repeat("a", 64)), Digest: strings.Repeat("b", 64)}
	raw, _ := json.Marshal(scope)
	done := make(chan struct{})
	g := &Init{inputs: &inputPhase{late: true, control: inputtransfer.Control{Scope: scope}, done: done}}
	status, err := g.inputStatus(raw)
	if err != nil || !status.Found || status.Joined || status.Complete {
		t.Fatal("active operation claimed completion", err)
	}
	g.inputs.success = true
	status, err = g.inputStatus(raw)
	if err != nil || status.Complete {
		t.Fatal("unjoined operation claimed completion", err)
	}
	close(done)
	status, err = g.inputStatus(raw)
	if err != nil || !status.Complete || !status.Joined {
		t.Fatal("exact joined operation lost completion", err)
	}
	other := scope
	other.Nonce = api.InputNonce(strings.Repeat("c", 64))
	foreign, _ := json.Marshal(other)
	if _, err := g.inputStatus(foreign); err == nil {
		t.Fatal("foreign scope acquired completion")
	}
	if !g.inputs.success || g.commandHolds != 0 || len(g.owned) != 0 {
		t.Fatal("read-only status changed guest execution")
	}
}
