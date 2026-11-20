package e2b

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

func TestInputErrorTailKeepsMetadata(t *testing.T) {
	original := &client.UserError{
		Msg: "the input was refused", Hint: "Check its source.",
		Code: "fixture_refusal", Kind: client.KindRefused,
		Data: json.RawMessage(`{"source":"stdin"}`),
	}
	input := &inputError{UserError: original, err: errDisk}
	for _, tail := range []string{"", "source-reason"} {
		got := input.withTail(tail)
		var userErr *client.UserError
		if !errors.As(got, &userErr) {
			t.Fatalf("tail %q lost the user error: %v", tail, got)
		}
		want := original.Msg
		if tail != "" {
			want += ": " + tail
		}
		if userErr.Msg != want || userErr.Hint != original.Hint || userErr.Code != original.Code ||
			userErr.Kind != original.Kind || !bytes.Equal(userErr.Data, original.Data) || !errors.Is(got, errDisk) {
			t.Fatalf("tail %q changed error metadata or cause: %+v", tail, userErr)
		}
		if original.Msg != "the input was refused" {
			t.Fatal("adding a diagnostic changed the original error")
		}
	}
}

func requireInputClassification(t *testing.T, err error, code string) {
	t.Helper()
	var userErr *client.UserError
	if !errors.As(err, &userErr) || userErr.Code != code || userErr.Kind != client.KindFailed {
		t.Fatalf("input error = %+v, want code %q and kind failed", userErr, code)
	}
}
