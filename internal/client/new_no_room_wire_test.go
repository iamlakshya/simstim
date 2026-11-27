package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This uses the actual ordered frame writer and pending response consumer.
// The existing generated transport stamp isolates local Source/OP semantics.
// It proves neither native memory admission nor real SSH authentication.
func TestNewNoRoomActualFrameAndReplyUseFreshOriginalSourceScope(t *testing.T) {
	for _, kind := range []string{"same request", "other nonce", "other member", "other host", "future version"} {
		t.Run(kind, func(t *testing.T) {
			host := h5GrantState(t)
			conn := finishGeneratedTransportFixture(t, host, strings.Repeat("a", 32), "session.id", api.FeatureNewNoRoomSettlementV1)
			selection, err := PrepareHostSelection(t.Context(), "box", NewInputOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer selection.Close()
			work, cancel := context.WithCancel(t.Context())
			defer cancel()
			r := NewGrantRequest(false, false)
			defer r.Close()
			plan := &HostAttemptPlanV1{ctx: work, cancel: cancel, selection: selection, args: NewArgs{Agent: "shell"}}
			attempt := &HostAttemptV1{plan: plan, target: host.Name, host: host, conn: conn, request: r, cancel: cancel,
				ready: true, newCalled: true, running: make(chan struct{})}
			r.hostAttempt, plan.active = attempt, attempt
			bound, err := selection.BindNewRequest(work, r, host.Name)
			if err != nil {
				t.Fatal(err)
			}
			attempt.ctx = bound
			publication, err := hostAttemptNewPublicationContextV1(bound, attempt)
			if err != nil {
				t.Fatal(err)
			}
			physical, release, err := conn.beginGrant(publication, "new refusal frame fixture", false)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			var output bytes.Buffer
			conn.w, conn.pending = proto.NewWriter(&output), map[uint64]chan proto.Response{}
			id, pending, err := conn.startCall(physical, "new", NewArgs{ID: "abcd2345", Agent: "shell", Wall: "none"})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.drop(id)
			release() // The first writer's real native OP has ended before await.
			frame, err := proto.ReadFrom(&output)
			if err != nil {
				t.Fatal(err)
			}
			var request proto.Request
			if frame.Type != proto.TRequest || json.Unmarshal(frame.Payload, &request) != nil || !api.ValidNewAttemptNonceV1(request.NewAttempt) {
				t.Fatal("actual writer lost its original nonce")
			}
			settlement := api.NewNoRoomSettlementV1{Version: 1, RequestID: id, SessionID: "abcd2345", HostID: conn.HostID(), Attempt: request.NewAttempt, Outcome: "never-dispatched"}
			switch kind {
			case "other nonce":
				settlement.Attempt = strings.Repeat("c", 32)
			case "other member":
				settlement.SessionID = "bcde2345"
			case "other host":
				settlement.HostID = strings.Repeat("c", 32)
			case "future version":
				settlement.Version = 2
			}
			data, _ := json.Marshal(api.NewNoRoomEnvelopeV1{Settlement: settlement, Room: json.RawMessage(`{"accounting_known":true,"room":1,"needed":2}`)})
			var input bytes.Buffer
			if err := proto.NewWriter(&input).WriteJSON(proto.TResponse, proto.Response{ID: id, Error: &proto.Error{Code: "no_room", Message: "no room", Hint: "Choose another host.", Data: data}}); err != nil {
				t.Fatal(err)
			}
			answer, err := proto.ReadFrom(&input)
			if err != nil {
				t.Fatal(err)
			}
			var response proto.Response
			if json.Unmarshal(answer.Payload, &response) != nil {
				t.Fatal("actual response decode failed")
			}
			pending <- response
			returned := conn.awaitCall(physical, "new", id, pending, nil)
			var proof *settledNewRefusalV1
			if got := errors.As(returned, &proof); got != (kind == "same request") {
				t.Fatal("correlated actual reply proof", kind, got, returned)
			}
			var user *UserError
			if !errors.As(returned, &user) || strings.Contains(string(user.Data), request.NewAttempt) || strings.Contains(string(user.Data), "new_settlement") {
				t.Fatal("control envelope reached public diagnostics", returned)
			}
			if proof != nil {
				before := proof.room
				user.Data = json.RawMessage(`{"room":999999999}`)
				if proof.room != before {
					t.Fatal("mutable public error changed private room evidence")
				}
			}
		})
	}
}
