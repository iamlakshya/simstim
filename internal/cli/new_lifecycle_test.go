package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestNewStartStepRetainsAndCancelsItsActualAttemptBudget(t *testing.T) {
	request := client.NewGrantRequest(false, false)
	owner := &newStartCleanup{request: request, budget: t.Context()}
	cmd := &cobra.Command{}
	cmd.SetContext(context.WithValue(client.WithGrantRequest(t.Context(), request), newStartCleanupKey{}, owner))
	want := errors.New("preparation refused")
	var actual context.Context
	err := newStartStep(cmd, func(ctx context.Context) error { actual = ctx; return want })
	if !errors.Is(err, want) || actual != owner.budget || !errors.Is(actual.Err(), context.Canceled) {
		t.Fatalf("actual attempt budget lost or left active: err=%v ctx=%v", err, actual.Err())
	}
	if _, ok := actual.Deadline(); !ok {
		t.Fatal("the original attempt had no bounded cleanup deadline")
	}
	if err := owner.finish(); err != nil {
		t.Fatal(err)
	}
	if err := owner.finish(); err != nil {
		t.Fatal("repeated release changed its result", err)
	}
}
