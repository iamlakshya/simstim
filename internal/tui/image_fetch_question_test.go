package tui

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/client"
)

type imageFetchQuestionConnV1 struct {
	Conn
	calls   atomic.Int32
	entered chan context.Context
}

func (c *imageFetchQuestionConnV1) ResolveNewWall(_ context.Context, a client.NewArgs) (client.NewArgs, error) {
	a.Wall = "microvm"
	return a, nil
}

func (c *imageFetchQuestionConnV1) PrepareNewVM(ctx context.Context, _ client.NewArgs, _ string) (*client.VMNeedPlanV1, error) {
	c.calls.Add(1)
	c.entered <- ctx
	return nil, &client.ConfirmError{Host: "gcp-dev", Kind: client.ConfirmYesNo, Prompt: "gcp-dev lacks agent-base@12345678. Get it now?", Grant: client.Outbound{Kind: client.OutboundImageFetchV1, Name: "agent-base@12345678"}, Err: &client.UserError{Msg: "the original pinned image is missing", Hint: "Approve the image in this terminal.", Code: "image_fetch_required", Kind: client.KindRefused}}
}

func TestNewImageFetchQuestionWaitsBeforeLauncherValuesAndDeclineStopsStart(t *testing.T) {
	hs := newPlacementFixture(t, "gcp-dev")
	hs.key("n")
	dialog := hs.m.modal.(*newSession)
	host := hs.m.hostByName(dialog.host)
	connection := &imageFetchQuestionConnV1{Conn: unclean(host.conn), entered: make(chan context.Context, 1)}
	host.conn = cleanConn{connection}
	request := client.NewGrantRequest(false, false)
	t.Cleanup(func() { _ = request.Close() })
	dialog.starting = true
	hs.do(hs.m.startSessionWithSource(host, startReq{host: host.name(), args: client.NewArgs{Agent: "shell", Cwd: "~", Name: "image-question", Wall: "microvm"}, grants: request, form: dialog, wallIntent: "microvm"}))
	sheet, ok := hs.m.modal.(*grantQuestion)
	if !ok || sheet.request.question.Grant.Kind != client.OutboundImageFetchV1 {
		t.Fatal("missing image did not open its existing live grant sheet")
	}
	original := <-connection.entered
	if original.Err() != nil || connection.calls.Load() != 1 || len(hs.be.secretsFrom) != 0 || len(hs.be.newArgs) != 0 {
		t.Fatal("image question ended its source or selected values before an answer")
	}
	hs.key("n")
	if connection.calls.Load() != 1 || len(hs.be.secretsFrom) != 0 || len(hs.be.newArgs) != 0 || unclean(host.conn) != connection {
		t.Fatal("declined image retried, sent values or replaced its original connection")
	}
	if original.Err() == nil {
		t.Fatal("declined image kept its original attempt alive")
	}
}
