package host

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestStopBridgeAdvertisesAndRejectsAmbiguousRequests(t *testing.T) {
	if !slices.Contains(Features, "session.stop") {
		t.Fatal("the implemented stop entrypoint is unreachable from the client")
	}
	for _, raw := range []string{`{}`, `null`, `{"id":"a2a2a2a2","ID":"b3b3b3b3"}`, `{"id":"a2a2a2a2","force":true}`, `{"id":"../outside"}`, `{"id":"a2a2a2a2"} {}`} {
		var output bytes.Buffer
		b := &bridge{ctx: context.Background(), w: proto.NewWriter(&output)}
		if !b.handleStopV1(proto.Request{ID: 4, Op: "stop", Args: json.RawMessage(raw)}) {
			t.Fatal("the real stop dispatcher did not recognise its operation")
		}
		frame, err := proto.Read(bufio.NewReader(&output))
		var response proto.Response
		if err != nil || frame.Type != proto.TResponse || json.Unmarshal(frame.Payload, &response) != nil || response.OK || response.Error == nil || response.Error.Code != "bad_request" || response.Error.Hint == "" {
			t.Fatalf("ambiguous stop request reached native ownership: %s: %+v %v", raw, response, err)
		}
	}
}

func TestStopCancellationCannotBeginNativeOwnership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	row, err := StopContext(ctx, "a2a2a2a2")
	if row != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("a cancelled stop returned a row or lost cancellation", err)
	}
}
