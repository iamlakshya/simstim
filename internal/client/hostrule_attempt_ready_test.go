package client

import (
	"context"
	"testing"
)

func TestHostAttemptReadyRejectsAQuestionOrPartialConnection(t *testing.T) {
	for _, kind := range []string{"no attempt", "partial dial", "failed facts", "closed", "other active"} {
		t.Run(kind, func(t *testing.T) {
			plan, attempt := attemptResultFixtureV1(t)
			switch kind {
			case "no attempt":
				attempt = nil
			case "partial dial":
				attempt.conn = &Conn{}
			case "failed facts":
				attempt.conn, attempt.ready = &Conn{}, false
			case "closed":
				attempt.ready, attempt.closed = true, true
			case "other active":
				plan.active = &HostAttemptV1{}
			}
			if err := attempt.CheckReadyV1(context.Background()); err == nil {
				t.Fatal("an incomplete actual owner passed the direct readiness boundary")
			}
		})
	}
}

func TestHostAttemptIntakeRefusesUnfinishedAutoBeforeSelection(t *testing.T) {
	plan, attempt := attemptResultFixtureV1(t)
	plan.selection = &HostSelectionV1{}
	attempt.request.hostAttempt = attempt
	attempt.ready = false
	if err := checkHostAttemptIntakeV1(attempt.Context(), NewArgs{}, nil); err == nil {
		t.Fatal("unfinished facts allowed input or value selection")
	}
	if attempt.request.preparationSettings != nil || attempt.request.newInputs != nil || attempt.request.newSecrets != nil {
		t.Fatal("readiness refusal selected values or opened another source")
	}
}
