package tui

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

type earlyNewPreparationQuestionV1 struct {
	Conn
	host    string
	stage   string
	calls   atomic.Int32
	entered chan context.Context
}

func (c *earlyNewPreparationQuestionV1) step(ctx context.Context, a client.NewArgs, stage string) (client.NewArgs, error) {
	if c.stage != stage {
		return a, nil
	}
	c.calls.Add(1)
	c.entered <- ctx
	return client.NewArgs{Agent: "replacement"}, &client.ConfirmError{Host: c.host, Kind: client.ConfirmYesNo,
		Prompt: "Approve this original preparation?", Grant: client.Outbound{Kind: client.OutboundSecret, Name: "TOKEN"},
		Err: &client.UserError{Msg: "the original preparation needs an answer", Hint: "Answer in this terminal.", Code: "grant_confirmation", Kind: client.KindRefused}}
}
func (c *earlyNewPreparationQuestionV1) PrepareNewInputPolicy(ctx context.Context, a client.NewArgs, _ client.NewInputOptions) (client.NewArgs, error) {
	return c.step(ctx, a, "input policy")
}
func (c *earlyNewPreparationQuestionV1) PrepareNewPolicy(ctx context.Context, a client.NewArgs) (client.NewArgs, error) {
	return c.step(ctx, a, "policy")
}
func (c *earlyNewPreparationQuestionV1) ResolveNewWall(ctx context.Context, a client.NewArgs) (client.NewArgs, error) {
	return c.step(ctx, a, "wall")
}
func (c *earlyNewPreparationQuestionV1) PrepareNewInputs(ctx context.Context, a client.NewArgs) (client.NewArgs, error) {
	return c.step(ctx, a, "inputs")
}
func (c *earlyNewPreparationQuestionV1) PrepareNewSecrets(ctx context.Context, a client.NewArgs, _ ...*client.VMNeedPlanV1) (client.NewArgs, error) {
	return c.step(ctx, a, "secrets")
}

// In-memory launcher regression only. No fake permission or native admission
// is issued. Each real UI question must stay live and decline before values.
func TestNewEarlyPreparationQuestionsStayLiveAndDeclineBeforeValues(t *testing.T) {
	for _, stage := range []string{"input policy", "policy", "wall", "inputs", "secrets"} {
		t.Run(stage, func(t *testing.T) {
			hs := newPlacementFixture(t, "")
			if err := client.SetDefaultHost(t.Context(), hs.be.hosts[0].Name); err != nil {
				t.Fatal(err)
			}
			hs.key("n")
			dialog := hs.m.modal.(*newSession)
			host := hs.m.hostByName(dialog.host)
			connection := &earlyNewPreparationQuestionV1{Conn: unclean(host.conn), host: host.name(), stage: stage, entered: make(chan context.Context, 1)}
			host.conn = cleanConn{connection}
			request := client.NewGrantRequest(false, false)
			t.Cleanup(func() { _ = request.Close() })
			dialog.starting = true
			hs.do(hs.m.startSessionWithSource(host, startReq{host: host.name(), args: client.NewArgs{Agent: "shell", Cwd: "~", Name: "question", Wall: "none"}, grants: request, form: dialog, wallIntent: "none"}))
			if _, ok := hs.m.modal.(*grantQuestion); !ok {
				t.Fatal("early preparation bypassed the existing live question sheet", stage)
			}
			original := <-connection.entered
			if original.Err() != nil || connection.calls.Load() != 1 || len(hs.be.secretsFrom) != 0 || len(hs.be.newArgs) != 0 {
				t.Fatal("question cancelled its source or selected values before the answer", stage)
			}
			hs.key("n")
			if connection.calls.Load() != 1 || len(hs.be.secretsFrom) != 0 || len(hs.be.newArgs) != 0 || unclean(host.conn) != connection {
				t.Fatal("decline retried, fetched values or replaced the original connection", stage)
			}
			if original.Err() == nil {
				t.Fatal("declined preparation retained its active operation", stage)
			}
		})
	}
}
