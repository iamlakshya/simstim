package egress

import (
	"encoding/base64"
	"net/http"
	"testing"
)

func testWebSocketKey() string {
	return base64.StdEncoding.EncodeToString([]byte("the sample nonce"))
}

func wsUpgradeFixture() (*http.Request, *http.Response) {
	request := &http.Request{Method: http.MethodGet, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{
		"Connection": {"keep-alive, Upgrade"}, "Upgrade": {"websocket"},
		"Sec-Websocket-Key": {testWebSocketKey()}, "Sec-Websocket-Version": {"13"},
		"Sec-Websocket-Protocol": {"chat, superchat"}, "Sec-Websocket-Extensions": {"permessage-deflate"},
	}}
	response := &http.Response{StatusCode: http.StatusSwitchingProtocols, Header: http.Header{
		"Connection": {"Upgrade"}, "Upgrade": {"websocket"},
		"Sec-Websocket-Accept": {"s3pPLMBiTxaQ9kYGzzhZRbK+xOo="}, "Sec-Websocket-Protocol": {"chat"},
	}}
	return request, response
}

func TestBoundWebSocketHandshakeDisablesOnlyExtensions(t *testing.T) {
	request, response := wsUpgradeFixture()
	request.Header["sec-websocket-extensions"] = []string{"other-extension"}
	upgraded, err := prepareBoundWebSocketRequest(request)
	if err != nil || !upgraded || request.Header.Get("Sec-Websocket-Protocol") != "chat, superchat" {
		t.Fatal("valid classic upgrade or requested subprotocol was changed")
	}
	for name := range request.Header {
		if name == "Sec-Websocket-Extensions" || name == "sec-websocket-extensions" {
			t.Fatal("extension alias remained in origin request")
		}
	}
	if err := validateBoundWebSocketResponse(request, response); err != nil {
		t.Fatal(err)
	}
	ordinary := &http.Request{Method: http.MethodGet, ProtoMajor: 2, Header: http.Header{"Accept-Encoding": {"gzip"}}}
	if upgraded, err := prepareBoundWebSocketRequest(ordinary); err != nil || upgraded || ordinary.Header.Get("Accept-Encoding") != "gzip" {
		t.Fatal("ordinary H2 request was changed")
	}
}

func TestBoundWebSocketRejectsUnsolicitedExtensionsAndBadHandshakes(t *testing.T) {
	for _, mutate := range []func(*http.Response){
		func(r *http.Response) { r.Header.Set("Sec-Websocket-Extensions", "permessage-deflate") },
		func(r *http.Response) { r.Header["sec-websocket-extensions"] = []string{"unknown"} },
		func(r *http.Response) { r.Header.Set("Sec-Websocket-Accept", "wrong") },
		func(r *http.Response) { r.Header["sec-websocket-accept"] = []string{"s3pPLMBiTxaQ9kYGzzhZRbK+xOo="} },
		func(r *http.Response) { r.Header.Set("Sec-Websocket-Protocol", "unrequested") },
		func(r *http.Response) { r.Header.Set("Sec-Websocket-Protocol", "chat, superchat") },
		func(r *http.Response) { r.Header.Set("Sec-Websocket-Protocol", "chat\x00") },
		func(r *http.Response) { r.Header.Set("Upgrade", "different") },
		func(r *http.Response) { r.Header.Set("Content-Encoding", "gzip") },
		func(r *http.Response) { r.Header.Set("Content-Length", "0") },
		func(r *http.Response) { r.Header.Set("Transfer-Encoding", "chunked") },
		func(r *http.Response) { r.Trailer = http.Header{"X-Metadata": nil} },
		func(r *http.Response) { r.Header.Del("Connection") },
	} {
		request, response := wsUpgradeFixture()
		mutate(response)
		if err := validateBoundWebSocketResponse(request, response); err == nil {
			t.Fatal("invalid origin handshake passed before guest 101")
		}
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Method = http.MethodPost },
		func(r *http.Request) { r.ProtoMajor = 2 },
		func(r *http.Request) { r.ProtoMinor = 0 },
		func(r *http.Request) { r.Header.Set("Sec-Websocket-Key", "short") },
		func(r *http.Request) { r.Header.Set("Sec-Websocket-Key", testWebSocketKey()+"\n") },
		func(r *http.Request) { r.Header["sec-websocket-key"] = []string{testWebSocketKey()} },
		func(r *http.Request) { r.Header.Set("Sec-Websocket-Version", "12") },
		func(r *http.Request) { r.Header["Upgrade"] = []string{"websocket", "other"} },
	} {
		request, _ := wsUpgradeFixture()
		mutate(request)
		if _, err := prepareBoundWebSocketRequest(request); err == nil {
			t.Fatal("invalid guest handshake passed")
		}
	}
}
