package egress

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestModelGETBodiesUnknownUpgradesAndUnsupportedFileHostsNeverReachOrigin(t *testing.T) {
	grant := api.Rule{Layer: api.LayerSession, Allow: true, Target: "chatgpt.com", Tools: []string{"files"}}
	for _, test := range []struct{ method, host, path, body, upgrade string }{
		{"POST", "chatgpt.com", "/v1/files", "authored", ""},
		{"GET", "api.openai.com", "/v1/models", `{"tools":[{"type":"mcp"}]}`, ""},
		{"GET", "api.openai.com", "/v1/responses", `{"input":[]}`, "websocket"},
		{"POST", "api.openai.com", "/v1/responses", `{"input":[]}`, "h2c"},
		{"POST", "api.openai.com", "/v1/responses", `{"input":[]}`, "custom"},
	} {
		t.Run(test.method+test.host+test.path+test.upgrade, func(t *testing.T) {
			f := newModelHTTPFixture(t, "codex", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{grant}}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
			request := httptest.NewRequest(test.method, "http://"+test.host+":443"+test.path, bytes.NewBufferString(test.body)).WithContext(t.Context())
			if test.upgrade != "" {
				request.Header.Set("Connection", "Upgrade")
				request.Header.Set("Upgrade", test.upgrade)
				request.Header.Set("Sec-WebSocket-Version", "13")
				request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
			}
			response := httptest.NewRecorder()
			f.env.srv.proxyHTTPRequest(response, request, StreamHeader{Via: "explicit", Host: test.host, Port: 443}, "http", nil)
			if response.Code != 403 || response.Header().Get("X-Should-Retry") != "false" || f.lookups.Load() != 0 || f.dials.Load() != 0 {
				t.Fatal("malformed model protocol reached origin")
			}
		})
	}
}

func TestModelFileBodyDecodingAndTrailersFinishBeforeReservation(t *testing.T) {
	grant := api.Rule{Layer: api.LayerSession, Allow: true, Target: "api.openai.com", Tools: []string{"files"}, Once: true, GrantID: "files-once"}
	f := newModelHTTPFixture(t, "shell", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{grant}}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "authored file" {
			http.Error(w, "changed upload", 500)
			return
		}
		w.WriteHeader(201)
	}))
	valid := encodedBodyFixture(t, "gzip", bytes.NewBufferString("authored file"))
	corrupt := bytes.Clone(valid)
	corrupt[len(corrupt)-1] ^= 0xff
	request := httptest.NewRequest("POST", "http://api.openai.com:443/v1/files", bytes.NewReader(corrupt)).WithContext(t.Context())
	request.Header["Content-Encoding"] = []string{"identity", "gzip"}
	response := httptest.NewRecorder()
	f.env.srv.proxyHTTPRequest(response, request, StreamHeader{Via: "explicit", Host: "api.openai.com", Port: 443}, "http", nil)
	if response.Code != 403 || f.lookups.Load() != 0 || f.dials.Load() != 0 {
		t.Fatal("invalid file coding reached an origin")
	}
	for _, slot := range snapshotDNSLedger(f.env.srv.currentEngine()) {
		if slot.state != slotAvailable || slot.owner != nil {
			t.Fatal("invalid upload spent a files-once lease")
		}
	}
	good, _ := f.request(t, "POST", "api.openai.com", "/v1/files", valid, "gzip")
	if good.Code != 201 || f.requests.Load() != 1 {
		t.Fatal("body rejection consumed the later valid upload's grant")
	}
}

func TestModelFlatWebSocketEnvelopeRejectsUnverifiedNestedShape(t *testing.T) {
	engine := modelTestEngine(t, "codex", api.PresetOpen)
	request := Request{Host: "api.openai.com", Port: 443, Method: "GET", Path: "/v1/responses"}
	model := &modelRequest{request: request, guard: engine.modelGuard(request), webSocket: true}
	if err := model.inspect([]byte(`{"type":"response.create","response":{"tools":[{"type":"mcp"}],"input":[]}}`), true); err == nil {
		t.Fatal("unsupported nested envelope skipped the flat guards")
	}
}

func TestModelFileBombAndLateTrailerFailBeforeOriginAndOnce(t *testing.T) {
	grant := api.Rule{Layer: api.LayerSession, Allow: true, Target: "api.openai.com", Tools: []string{"files"}, Once: true, GrantID: "unspent-files"}
	f := newModelHTTPFixture(t, "shell", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{grant}}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	bomb := encodedBodyFixture(t, "gzip", io.LimitReader(repeatBodyByte{}, maxDecodedHTTPBody+1))
	response, _ := f.request(t, "POST", "api.openai.com", "/v1/files", bomb, "gzip")
	if response.Code != 403 || f.lookups.Load() != 0 || f.dials.Load() != 0 {
		t.Fatal("oversized decoded file reached origin")
	}
	guest, proxy := net.Pipe()
	defer guest.Close()
	_ = guest.SetDeadline(time.Now().Add(10 * time.Second))
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer proxy.Close()
		f.env.srv.ServeFlow(t.Context(), proxy, StreamHeader{Via: "explicit", Host: "api.openai.com", Port: 443})
	}()
	payload := "authored file"
	wire := fmt.Sprintf("POST http://api.openai.com:443/v1/files HTTP/1.1\r\nHost: api.openai.com:443\r\nTransfer-Encoding: chunked\r\nTrailer: X-Safe\r\n\r\n%x\r\n%s\r\n0\r\nAuthorization: forbidden\r\n\r\n", len(payload), payload)
	written := make(chan struct{})
	go func() { defer close(written); _, _ = io.WriteString(guest, wire) }()
	reply, err := http.ReadResponse(bufio.NewReader(guest), &http.Request{Method: "POST"})
	if err == nil {
		_, _ = io.Copy(io.Discard, reply.Body)
		_ = reply.Body.Close()
		if reply.StatusCode < 400 {
			t.Fatal("late forbidden trailer was accepted")
		}
	}
	_ = guest.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("trailer refusal did not close its owned stream")
	}
	<-written
	if f.lookups.Load() != 0 || f.dials.Load() != 0 {
		t.Fatal("late file trailer reached origin")
	}
	for _, slot := range snapshotDNSLedger(f.env.srv.currentEngine()) {
		if slot.state != slotAvailable || slot.owner != nil {
			t.Fatal("invalid file body spent its conditional once lease")
		}
	}
}
