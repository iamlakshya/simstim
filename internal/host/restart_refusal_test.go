package host

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/iamlakshya/simstim/internal/proto"
	"testing"
)

func TestRestartKeepsFinalAdmissionDataAndHint(t *testing.T) {
	for _, code := range []string{"no_room", dangerRefused} {
		original := &proto.Error{Code: code, Message: "the final native gate refused the start", Hint: "Wait for room, then review the request.", Data: json.RawMessage(`{"room":100,"asked":200}`)}
		wrapped := errors.Join(fmt.Errorf("restart publication: %w", original), errors.New("cleanup retained its original owner"))
		if got := restartRefusal(wrapped); got != original || string(got.Data) != string(original.Data) || got.Hint != original.Hint {
			t.Fatal("restart replaced the native admission result")
		}
	}
	if restartRefusal(nil) != nil || restartRefusal(errors.New("unrelated failure")) != nil || restartRefusal(&proto.Error{Code: "start_failed"}) != nil {
		t.Fatal("ordinary failure became an admission result")
	}
}
