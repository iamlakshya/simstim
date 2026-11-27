package client

import (
	"context"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestPullReviewRequiresAcknowledgedMetadataBeforeLiteralCommands(t *testing.T) {
	failure := errors.New("no original local receipt")
	commands := 0
	_, err := combinedPullReviewValues(t.Context(), func(context.Context) ([]api.Secret, error) { return nil, failure }, func(context.Context) ([][]byte, error) { commands++; return nil, nil })
	if !errors.Is(err, failure) || commands != 0 {
		t.Fatalf("unacknowledged review reached commands: %d %v", commands, err)
	}
}

func TestPullReviewCombinesFreshLiteralAndOwnedCurrentRetiredStandIns(t *testing.T) {
	values, err := combinedPullReviewValues(t.Context(), func(context.Context) ([]api.Secret, error) {
		return []api.Secret{"current-public", "retired-public"}, nil
	}, func(context.Context) ([][]byte, error) { return [][]byte{[]byte("fresh-current-literal")}, nil })
	if err != nil || len(values) != 3 || string(values[0]) != "fresh-current-literal" || string(values[1]) != "current-public" || string(values[2]) != "retired-public" {
		t.Fatal("complete literal/public review coverage was lost")
	}
	for _, value := range values {
		clear(value)
	}
}
