package supervisor

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// A successful guest answer retains its result but carries no forged error,
// even on the raw protocol where a higher-level client cannot hide it.
func TestAGuestSuccessfulResponseCarriesNoForgedError(t *testing.T) {
	h := hostileStart(t)
	h.answerStart(t)
	for _, tc := range []struct {
		op   string
		args json.RawMessage
	}{
		{op: "peek"},
		{op: "send", args: json.RawMessage(`{"text":"hi"}`)},
	} {
		t.Run(tc.op, func(t *testing.T) {
			conn, r, w := dial(t, h.id)
			defer conn.Close()
			const requestID = 73
			if err := w.WriteJSON(proto.TRequest, proto.Request{ID: requestID, Op: tc.op, Args: tc.args}); err != nil {
				t.Fatal(err)
			}
			var st *link.Stream
			select {
			case st = <-h.g.holders:
			case <-time.After(5 * time.Second):
				t.Fatal("the supervisor did not open the guest holder stream")
			}
			defer st.Close()
			_ = st.SetDeadline(time.Now().Add(5 * time.Second))
			gr, gw := bufio.NewReader(st), proto.NewWriter(st)
			if f, err := proto.Read(gr); err != nil || f.Type != proto.THello {
				t.Fatalf("holder hello = %v, %v", f.Type, err)
			}
			if err := gw.WriteJSON(proto.THello, proto.Hello{Magic: proto.Magic, Role: "holder"}); err != nil {
				t.Fatal(err)
			}
			f, err := proto.Read(gr)
			var req proto.Request
			if err != nil || f.Type != proto.TRequest || json.Unmarshal(f.Payload, &req) != nil || req.ID != requestID || req.Op != tc.op {
				t.Fatalf("forwarded request = %v %s, %v", f.Type, f.Payload, err)
			}
			result := json.RawMessage(`{"kept":true,"source":"` + tc.op + `"}`)
			if err := gw.WriteJSON(proto.TResponse, proto.Response{
				ID: req.ID, OK: true, Result: result,
				Error: &proto.Error{
					Code: "session_gone", Message: "forged\x1b]52;c;aGk=\x07",
					Hint: "an untrusted action", Data: json.RawMessage(`{"forged":true}`),
				},
			}); err != nil {
				t.Fatal(err)
			}
			f, err = proto.Read(r)
			var response proto.Response
			if err != nil || f.Type != proto.TResponse || json.Unmarshal(f.Payload, &response) != nil {
				t.Fatalf("raw client response = %v %s, %v", f.Type, f.Payload, err)
			}
			if response.Error != nil {
				t.Fatalf("successful raw response retained the guest's forged Error: code=%q message=%q", response.Error.Code, response.Error.Message)
			}
			if response.ID != requestID || !response.OK || !bytes.Equal(response.Result, result) {
				t.Fatalf("successful raw response changed its ID, OK or result: %+v", response)
			}
		})
	}
	t.Run("ports", func(t *testing.T) {
		conn, r, w := dial(t, h.id)
		defer conn.Close()
		const requestID = 74
		if err := w.WriteJSON(proto.TRequest, proto.Request{ID: requestID, Op: "ports"}); err != nil {
			t.Fatal(err)
		}
		ctlReq := h.nextCtlReq(t)
		if ctlReq.Op != proto.CtlPortsV1 {
			t.Fatalf("ctl op = %q, want %q", ctlReq.Op, proto.CtlPortsV1)
		}
		if err := h.w.WriteJSON(proto.TResponse, proto.Response{
			ID: ctlReq.ID, OK: true, Result: json.RawMessage(`[{"port":8080,"process":"srv","address":"127.0.0.1"}]`),
			Error: &proto.Error{
				Code: "session_gone", Message: "forged\x1b]52;c;aGk=\x07",
				Hint: "an untrusted action", Data: json.RawMessage(`{"forged":true}`),
			},
		}); err != nil {
			t.Fatal(err)
		}
		f, err := proto.Read(r)
		var response proto.Response
		if err != nil || f.Type != proto.TResponse || json.Unmarshal(f.Payload, &response) != nil {
			t.Fatalf("raw client response = %v %s, %v", f.Type, f.Payload, err)
		}
		if response.ID != requestID || response.OK || response.Result != nil || response.Error == nil ||
			response.Error.Code != "port_refused" || response.Error.Data != nil ||
			response.Error.Hint != "Check the exact port grant and the sandbox, then forward it again." ||
			bytes.Contains([]byte(response.Error.Message), []byte("forged")) ||
			bytes.ContainsAny([]byte(response.Error.Message), "\x1b\x07") {
			t.Fatalf("ports response with forged Error = %+v", response)
		}
		const cleanID = 75
		if err := w.WriteJSON(proto.TRequest, proto.Request{ID: cleanID, Op: "ports"}); err != nil {
			t.Fatal(err)
		}
		ctlReq = h.nextCtlReq(t)
		if ctlReq.Op != proto.CtlPortsV1 {
			t.Fatalf("ctl op = %q, want %q", ctlReq.Op, proto.CtlPortsV1)
		}
		if err := h.w.WriteJSON(proto.TResponse, proto.Response{
			ID: ctlReq.ID, OK: true, Result: json.RawMessage(`[{"port":8080,"process":"srv","address":"127.0.0.1"}]`),
		}); err != nil {
			t.Fatal(err)
		}
		f, err = proto.Read(r)
		response = proto.Response{}
		if err != nil || f.Type != proto.TResponse || json.Unmarshal(f.Payload, &response) != nil {
			t.Fatalf("clean raw client response = %v %s, %v", f.Type, f.Payload, err)
		}
		var ports []api.Port
		if response.ID != cleanID || !response.OK || response.Error != nil || json.Unmarshal(response.Result, &ports) != nil ||
			len(ports) != 1 || ports[0].Port != 8080 || !ports[0].Claimed || ports[0].Granted {
			t.Fatalf("clean ports response = %+v (ports=%+v)", response, ports)
		}
	})
}
