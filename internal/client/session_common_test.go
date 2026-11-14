package client

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestNamedSessionKeepsTheGrantPublicationGate(t *testing.T) {
	h := h5GrantState(t)
	writes := 0
	c := &Conn{Host: h, hostID: strings.Repeat("a", 32), Features: []string{"session.id", "secrets"}, pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
	c.w = proto.NewWriter(grantWriteFunc(func(data []byte) (int, error) { writes++; return 0, io.ErrClosedPipe }))
	ctx := WithGrantRequest(t.Context(), NewGrantRequest(true, false))
	_, err := c.NewSession(ctx, NewArgs{Name: "valid-session-name", Secrets: map[string]api.Secret{"API_KEY": "generated-value"}})
	var refusal *UserError
	if !errors.As(err, &refusal) || refusal.Kind != KindRefused || refusal.Code != "tier_refused" || writes != 0 {
		t.Fatalf("name support bypassed the grant producer: writes=%d,error=%v", writes, err)
	}
}

func TestDeleteNamedCarriesTheExactConfirmedNameIncludingEmpty(t *testing.T) {
	for _, name := range []string{"", "plain", " raw\x1bc\nname "} {
		var wire []byte
		c := &Conn{Host: Host{Name: "box"}, pending: map[uint64]chan proto.Response{}, done: make(chan struct{})}
		c.w = proto.NewWriter(grantWriteFunc(func(data []byte) (int, error) { wire = bytes.Clone(data); return 0, io.ErrClosedPipe }))
		_, _ = c.DeleteNamed(t.Context(), "abc23def", name)
		frame, err := proto.Read(bufio.NewReader(bytes.NewReader(wire)))
		if err != nil || frame.Type != proto.TRequest {
			t.Fatalf("delete frame=%+v,error=%v", frame, err)
		}
		var req proto.Request
		if err := json.Unmarshal(frame.Payload, &req); err != nil || req.Op != "delete" {
			t.Fatalf("delete request=%+v,error=%v", req, err)
		}
		var args map[string]string
		if err := json.Unmarshal(req.Args, &args); err != nil {
			t.Fatal(err)
		}
		actual, present := args["name"]
		if !present || actual != name || args["id"] != "abc23def" {
			t.Fatalf("confirmed name changed or vanished: present=%v,id=%q,name=%q", present, args["id"], actual)
		}
	}
}
