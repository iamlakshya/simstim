package client

import (
	"context"
	"errors"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestForkAndRewindRefuseOldHelpersBeforeWriting(t *testing.T) {
	connection := &Conn{Host: Host{Name: "box"}}
	for _, run := range []func() error{
		func() error {
			_, err := connection.Fork(context.Background(), api.ForkArgs{ID: "parent23", Count: 1})
			return err
		},
		func() error { _, err := connection.Checkpoints(context.Background(), "parent23"); return err },
		func() error {
			_, err := connection.Rewind(context.Background(), api.RewindArgs{ID: "parent23", Checkpoint: "20260203T040506.000Z"})
			return err
		},
	} {
		var user *UserError
		if err := run(); !errors.As(err, &user) || user.Code != "helper_old" || user.Hint == "" {
			t.Errorf("old helper request = %v", err)
		}
	}
}

func TestForkAndRewindValidateBeforeRequestPublication(t *testing.T) {
	connection := &Conn{Features: []string{api.FeatureFork, api.FeatureRewind}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := connection.Fork(ctx, api.ForkArgs{ID: "parent23"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("default count did not reach the cancelled original request: %v", err)
	}
	for _, args := range []api.ForkArgs{{ID: "parent23", Count: -1}, {ID: "parent23", Count: 33}, {ID: "../../host", Count: 1}} {
		var user *UserError
		if _, err := connection.Fork(ctx, args); !errors.As(err, &user) || user.Kind != KindUsage {
			t.Errorf("invalid fork was not refused before publication: %+v %v", args, err)
		}
	}
	if _, err := connection.Rewind(ctx, api.RewindArgs{ID: "parent23", Checkpoint: "../disk"}); errors.Is(err, context.Canceled) {
		t.Fatal("path-like checkpoint reached request publication")
	}
	failure := api.ForkFailure{Index: 1, Error: proto.Error{Code: "no_room", Message: "\x1b[2Jno room", Hint: "choose\nelsewhere"}}
	var user *UserError
	if err := ForkFailureError(failure); !errors.As(err, &user) || user.Code != "no_room" || user.Msg == failure.Error.Message || user.Hint == failure.Error.Hint {
		t.Fatalf("child failure was not cleaned and mapped: %v", err)
	}
}
