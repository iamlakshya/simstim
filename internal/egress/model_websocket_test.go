package egress

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/iamlakshya/simstim/internal/api"
)

func openModelWebSocket(t *testing.T, f *modelHTTPFixture) (net.Conn, *bufio.Reader) {
	t.Helper()
	guest, proxy := net.Pipe()
	t.Cleanup(func() { _ = guest.Close(); _ = proxy.Close() })
	_ = guest.SetDeadline(time.Now().Add(10 * time.Second))
	go f.env.srv.ServeFlow(t.Context(), proxy, StreamHeader{Via: "explicit", Host: "api.openai.com", Port: 443})
	_, err := io.WriteString(guest, "GET http://api.openai.com:443/v1/responses HTTP/1.1\r\nHost: api.openai.com:443\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Extensions: permessage-deflate\r\n\r\n")
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(guest)
	response, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil || response.StatusCode != 101 || response.Header.Get("Sec-WebSocket-Extensions") != "" {
		t.Fatalf("model upgrade: %v, %v", response, err)
	}
	return guest, reader
}

func modelUpgradeOrigin(writer http.ResponseWriter, request *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	if request.Header.Get("Sec-WebSocket-Extensions") != "" {
		return nil, nil, errors.New("model origin received a compression offer")
	}
	conn, buffer, err := http.NewResponseController(writer).Hijack()
	if err != nil {
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, err = fmt.Fprint(buffer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n\r\n")
	if err == nil {
		err = buffer.Flush()
	}
	return conn, buffer, err
}

func TestModelWebSocketSharesFramingAndRefusesWholeMessageWithSafe1008(t *testing.T) {
	const allowed = `{"type":"response.create","tools":[{"type":"function","name":"local"}],"input":[]}`
	const refused = `{"type":"response.create","tools":[{"type":"code_interpreter"}],"input":[]}`
	originResult := make(chan error, 1)
	f := newModelHTTPFixture(t, "codex", api.Policy{Preset: api.PresetLocked}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffer, err := modelUpgradeOrigin(w, r)
		if err != nil {
			originResult <- err
			return
		}
		defer conn.Close()
		message, err := wsReadTestMessage(buffer)
		if err != nil || string(message) != allowed {
			originResult <- errors.New("allowed logical model message changed")
			return
		}
		if _, err = conn.Write(wsTestWire(1, true, false, []byte(`{"type":"response.created"}`))); err != nil {
			originResult <- err
			return
		}
		frame, err := wsReadTestFrame(buffer)
		if len(frame.payload) != 0 || err == nil {
			originResult <- errors.New("refused model message released bytes to origin")
			return
		}
		originResult <- nil
	}))
	guest, reader := openModelWebSocket(t, f)
	if _, err := guest.Write(wsTestWire(1, true, true, []byte(allowed))); err != nil {
		t.Fatal(err)
	}
	if message, err := wsReadTestMessage(reader); err != nil || !bytes.Contains(message, []byte("response.created")) {
		t.Fatalf("model reply: %q, %v", message, err)
	}
	// The prohibited capability is after an innocent first fragment. No
	// prefix of that logical request may cross the checked origin.
	cut := len(refused) / 2
	wire := append(wsTestWire(1, false, true, []byte(refused[:cut])), wsTestWire(0, true, true, []byte(refused[cut:]))...)
	if _, err := guest.Write(wire); err != nil {
		t.Fatal(err)
	}
	closeFrame, err := wsReadTestFrame(reader)
	if err != nil || closeFrame.opcode != 8 || len(closeFrame.payload) < 2 || binary.BigEndian.Uint16(closeFrame.payload[:2]) != 1008 || len(closeFrame.payload) > 125 || !utf8.Valid(closeFrame.payload[2:]) || (!strings.Contains(string(closeFrame.payload[2:]), "code_interpreter") || !strings.Contains(string(closeFrame.payload[2:]), "Try:")) {
		t.Fatalf("unsafe model refusal close: %+v, %v", closeFrame, err)
	}
	select {
	case err := <-originResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("model origin did not close")
	}
	found := false
	for _, audit := range f.env.waitAudits(t, 1) {
		if audit.Decision == "deny" && strings.HasPrefix(audit.Rule, "model:code_execution/") && strings.Contains(audit.Rule, "code_interpreter") {
			found = true
		}
	}
	if !found {
		t.Fatal("model message refusal lost its egress_blocked audit input")
	}
}

func TestModelWebSocketAcquiresOnceOnOriginalConnectedPermitAndRevokesLive(t *testing.T) {
	const request = `{"type":"response.create","tools":[{"type":"mcp"}],"input":[]}`
	received := make(chan error, 1)
	closed := make(chan struct{})
	grant := api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "api.openai.com", Tools: []string{"mcp"}, Once: true, GrantID: "websocket-model-lease"}
	f := newModelHTTPFixture(t, "codex", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{grant}}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, buffer, err := modelUpgradeOrigin(w, r)
		if err != nil {
			received <- err
			return
		}
		defer conn.Close()
		defer close(closed)
		message, err := wsReadTestMessage(buffer)
		if err == nil && string(message) != request {
			err = errors.New("model capability changed its message")
		}
		received <- err
		if err != nil {
			return
		}
		var probe [1]byte
		_, _ = buffer.Read(probe[:])
	}))
	guest, _ := openModelWebSocket(t, f)
	if _, err := guest.Write(wsTestWire(1, true, true, []byte(request))); err != nil {
		t.Fatal(err)
	}
	if err := <-received; err != nil {
		t.Fatal(err)
	}
	engine := f.env.srv.currentEngine()
	other := engine.NewPermit()
	model := &modelRequest{request: Request{Host: "api.openai.com", Port: 443, Method: "GET", Path: "/v1/responses"}, features: []string{"mcp"}}
	if got := model.refusal(engine, other, evalReserve); got != "mcp" {
		t.Fatal("a second flow reused the connected WebSocket's once capability")
	}
	f.env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: api.Policy{Preset: api.PresetLocked}})
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("capability removal kept the old WebSocket origin alive")
	}
}
