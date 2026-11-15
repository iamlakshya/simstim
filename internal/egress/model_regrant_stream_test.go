package egress

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestModelOnceAppendKeepsTheOldWebSocketAndAdmitsOneNewFlow(t *testing.T) {
	const message = `{"type":"response.create","tools":[{"type":"mcp"}],"input":[]}`
	first := api.Rule{Layer: api.LayerSession, Allow: true, Target: "api.openai.com", Tools: []string{"mcp"}, Once: true, GrantID: "old-flow"}
	f := newModelHTTPFixture(t, "codex", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{first}}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffer, err := modelUpgradeOrigin(w, r)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			body, err := wsReadTestMessage(buffer)
			if err != nil || string(body) != message {
				return
			}
			if _, err := conn.Write(wsTestWire(1, true, false, []byte("ack"))); err != nil {
				return
			}
		}
	}))
	old, oldReader := openModelWebSocket(t, f)
	if _, err := old.Write(wsTestWire(1, true, true, []byte(message))); err != nil {
		t.Fatal(err)
	}
	if reply, err := wsReadTestMessage(oldReader); err != nil || !bytes.Equal(reply, []byte("ack")) {
		t.Fatal("initial capability flow was not admitted")
	}
	second := first
	second.GrantID = "next-flow"
	f.env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{first, second}}})
	if _, err := old.Write(wsTestWire(1, true, true, []byte(message))); err != nil {
		t.Fatal("append revoked the original WebSocket")
	}
	if reply, err := wsReadTestMessage(oldReader); err != nil || !bytes.Equal(reply, []byte("ack")) {
		t.Fatal("original permit lost its exact old grant")
	}
	next, nextReader := openModelWebSocket(t, f)
	if _, err := next.Write(wsTestWire(1, true, true, []byte(message))); err != nil {
		t.Fatal(err)
	}
	if reply, err := wsReadTestMessage(nextReader); err != nil || !bytes.Equal(reply, []byte("ack")) {
		t.Fatal("fresh independent once grant did not admit the next flow")
	}
	engine := f.env.srv.currentEngine()
	third := engine.NewPermit()
	defer third.Close()
	request := Request{Host: "api.openai.com", Port: 443, Method: "POST", Path: "/v1/responses"}
	if got := engine.admitModelRequest(&modelRequest{request: request, features: []string{"mcp"}}, third, true, evalReserve); got != "mcp" {
		t.Fatal("two active once instances admitted a third flow")
	}
}
