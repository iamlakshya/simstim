package host

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"

	"github.com/iamlakshya/simstim/internal/proto"
)

func TestBridgeDispatchesEveryPolicyOperation(t *testing.T) {
	for _, op := range []string{"policy.ls", "policy.add", "policy.rm", "policy.log"} {
		t.Run(op, func(t *testing.T) {
			var out bytes.Buffer
			b := &bridge{w: proto.NewWriter(&out)}
			b.handle(proto.Request{ID: 17, Op: op, Args: json.RawMessage(`{"id":"invalid"}`)})
			f, err := proto.Read(bufio.NewReader(&out))
			if err != nil || f.Type != proto.TResponse {
				t.Fatalf("policy reply = %+v, %v", f, err)
			}
			var response proto.Response
			if err := json.Unmarshal(f.Payload, &response); err != nil {
				t.Fatal(err)
			}
			if response.ID != 17 || response.OK || response.Error == nil || response.Error.Code == "unknown_op" || response.Error.Hint == "" {
				t.Fatalf("policy request did not reach its guarded handler: %+v", response)
			}
		})
	}
}

func TestBridgeRefusesEveryPolicyOperationInsideSession(t *testing.T) {
	for _, op := range []string{"policy.ls", "policy.add", "policy.rm", "policy.log"} {
		t.Run(op, func(t *testing.T) {
			var out bytes.Buffer
			b := &bridge{w: proto.NewWriter(&out)}
			payload, err := json.Marshal(proto.Request{ID: 23, Op: op})
			if err != nil {
				t.Fatal(err)
			}
			if !b.refuseInside(proto.Frame{Type: proto.TRequest, Payload: payload}) {
				t.Fatal("a session's policy request was admitted")
			}
			f, err := proto.Read(bufio.NewReader(&out))
			if err != nil {
				t.Fatal(err)
			}
			var response proto.Response
			if err := json.Unmarshal(f.Payload, &response); err != nil {
				t.Fatal(err)
			}
			if response.ID != 23 || response.OK || response.Error == nil || response.Error.Code != errCallerInside.Code {
				t.Fatalf("inside-session policy response = %+v", response)
			}
		})
	}
}
