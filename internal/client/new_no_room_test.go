package client

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func TestNewNoRoomControlNeverEntersPublicErrorData(t *testing.T) {
	nonce := strings.Repeat("b", 32)
	secretHash := strings.Repeat("c", 64)
	good := api.NewNoRoomEnvelopeV1{Settlement: api.NewNoRoomSettlementV1{Version: 1, RequestID: 3, SessionID: "abcd2345", HostID: strings.Repeat("a", 32), Attempt: nonce, Outcome: "never-dispatched"}, Room: json.RawMessage(`{"accounting_known":true,"room":1,"needed":2}`)}
	raw, _ := json.Marshal(good)
	for _, body := range [][]byte{raw, []byte(strings.Replace(string(raw), `"v":1`, `"v":999`, 1)), []byte(strings.Replace(string(raw), `"v":1`, `"v":1,"hash":"`+secretHash+`"`, 1)), []byte(`{"new_settlement":{"hash":"` + secretHash + `"}}`)} {
		returned := receiveHostAttemptNewRefusalV1(context.Background(), &Conn{Host: Host{Name: "box"}}, proto.Response{ID: 3, Error: &proto.Error{Code: "no_room", Message: "no room", Hint: "Choose another host.", Data: body}})
		var user *UserError
		if !errors.As(returned, &user) {
			t.Fatal(returned)
		}
		public, _ := json.Marshal(user)
		if strings.Contains(string(public), nonce) || strings.Contains(string(public), secretHash) || strings.Contains(string(public), "new_settlement") {
			t.Fatal("control metadata escaped through public diagnostics", string(public))
		}
		var proof *settledNewRefusalV1
		if errors.As(returned, &proof) {
			t.Fatal("a decoded control DTO minted retirement authority")
		}
	}
}

func TestNewNoRoomMissingOwnerAndCancelledReplyRemainUnproven(t *testing.T) {
	for _, ctx := range []context.Context{t.Context(), func() context.Context { ctx, cancel := context.WithCancel(t.Context()); cancel(); return ctx }()} {
		returned := receiveHostAttemptNewRefusalV1(ctx, &Conn{Host: Host{Name: "box"}}, proto.Response{ID: 3, Error: &proto.Error{Code: "no_room", Message: "no room", Hint: "Choose another host."}})
		var proof *settledNewRefusalV1
		if errors.As(returned, &proof) {
			t.Fatal("missing original writer/response produced a negative witness")
		}
	}
}
