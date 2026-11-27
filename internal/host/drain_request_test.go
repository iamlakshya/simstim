package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestDrainRequestRejectsAmbiguousOrMissingSettingsBeforeMutation(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `[]`, `{"on":false}`, `{"reason":""}`, `{"on":null,"reason":""}`, `{"on":false,"reason":null}`, `{"On":false,"reason":""}`, `{"on":false,"on":true,"reason":""}`, `{"on":false,"reason":"","operation":{}}`} {
		var output bytes.Buffer
		b := &bridge{w: proto.NewWriter(&output)}
		if !b.handleMore(proto.Request{ID: 42, Op: "host.drain", Args: json.RawMessage(raw)}) {
			t.Fatal("drain request was not dispatched")
		}
		frame, err := proto.Read(bufio.NewReader(&output))
		if err != nil {
			t.Fatal(err)
		}
		var response proto.Response
		if json.Unmarshal(frame.Payload, &response) != nil || response.OK || response.Error == nil || response.Error.Code != "bad_request" || response.Error.Hint == "" {
			t.Fatal("invalid setting reached the native writer", raw, response)
		}
	}
}
