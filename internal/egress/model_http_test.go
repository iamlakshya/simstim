package egress

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

// These are authored protocol fixtures, never agent traffic recordings. Every
// origin byte crosses an owned pipe through the actual checked transport.
type modelHTTPFixture struct {
	env                      *testEnv
	lookups, dials, requests atomic.Int64
}

func newModelHTTPFixture(t *testing.T, agent string, policy api.Policy, handler http.Handler) *modelHTTPFixture {
	t.Helper()
	f := new(modelHTTPFixture)
	dialer := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			f.lookups.Add(1)
			return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
		},
		DialTCP: func(_ context.Context, address netip.AddrPort) (net.Conn, error) {
			if address != netip.MustParseAddrPort("198.51.100.8:443") {
				return nil, errors.New("unchecked model fixture endpoint")
			}
			f.dials.Add(1)
			client, origin := net.Pipe()
			t.Cleanup(func() { _ = client.Close(); _ = origin.Close() })
			go serveHTTPConnection(t.Context(), origin, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				f.requests.Add(1)
				handler.ServeHTTP(w, r)
			}), nil)
			return client, nil
		},
	}
	f.env = startTestServer(t, dialer)
	f.env.call(t, CtlInit, InitArgs{SessionID: "model234567", SessionRef: "box/session", Agent: agent, Policy: policy})
	return f
}

func (f *modelHTTPFixture) request(t *testing.T, method, host, path string, body []byte, encoding string) (*httptest.ResponseRecorder, AuditEntry) {
	t.Helper()
	request := httptest.NewRequest(method, "http://"+host+":443"+path, bytes.NewReader(body)).WithContext(t.Context())
	request.Header.Set("Content-Type", "application/json")
	if encoding != "" {
		request.Header.Set("Content-Encoding", encoding)
	}
	writer := httptest.NewRecorder()
	audit := f.env.srv.proxyHTTPRequest(writer, request, StreamHeader{Via: "explicit", Host: host, Port: 443}, "http", nil)
	return writer, audit.get()
}

func TestModelHTTPRefusalUsesProvider403BeforeAnyOriginIO(t *testing.T) {
	for _, test := range []struct{ agent, host, path, body, errorType string }{
		{"claude", "api.anthropic.com", "/v1/messages", `{"tools":[{"type":"web_fetch_20260318"}]}`, "permission_error"},
		{"codex", "api.openai.com", "/v1/responses", `{"tools":[{"type":"mcp","server_url":"https://example.com"}]}`, "request_forbidden"},
		{"codex", "chatgpt.com", "/backend-api/codex/responses", `{"input":[{"type":"input_file","file_url":"https://example.com"}]}`, "request_forbidden"},
		{"claude", "api.anthropic.com", "/v1/messages/batches", `{"requests":[]}`, "permission_error"},
		{"codex", "api.openai.com", "/v1/files", `file bytes`, "request_forbidden"},
		{"codex", "api.openai.com", "/v1/responses", `{"tools":[{"type":"unknown_tool"}]}`, "request_forbidden"},
		{"codex", "api.openai.com", "/v1/responses", `{"input":`, "request_forbidden"},
	} {
		t.Run(test.agent+test.path+test.body, func(t *testing.T) {
			f := newModelHTTPFixture(t, test.agent, api.Policy{Preset: api.PresetLocked}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
			response, audit := f.request(t, "POST", test.host, test.path, []byte(test.body), "")
			if response.Code != 403 || response.Header().Get("X-Should-Retry") != "false" || !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("model refusal: %d, %v", response.Code, response.Header())
			}
			var shape map[string]json.RawMessage
			if err := json.Unmarshal(response.Body.Bytes(), &shape); err != nil || !strings.Contains(string(shape["error"]), test.errorType) {
				t.Fatalf("provider shape: %s, %v", response.Body.String(), err)
			}
			if test.agent == "claude" && (!strings.Contains(string(shape["type"]), "error") || !strings.Contains(string(shape["request_id"]), "req_")) {
				t.Fatal("Anthropic refusal lacks request identity")
			}
			if f.lookups.Load() != 0 || f.dials.Load() != 0 || f.requests.Load() != 0 || audit.Decision != "deny" || !strings.HasPrefix(audit.Rule, "model:") {
				t.Fatalf("refusal reached origin or lost audit: %+v", audit)
			}
		})
	}
}

func TestModelHTTPAllowedJSONPreservesBytesAndDefersScopedOnce(t *testing.T) {
	const body = ` {"input": [{"type":"message","content":[{"type":"input_text","text":"kept"}]}], "tools":[{"type":"mcp","server_url":"https://example.com"}]} `
	grant := api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "api.openai.com", Tools: []string{"mcp"}, Once: true, GrantID: "model-once"}
	// A shell has no agent builtin. This ONE tools rule must supply both net
	// and capability authority, with the ordinary TCP commit boundary.
	f := newModelHTTPFixture(t, "shell", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{grant}}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, err := io.ReadAll(r.Body)
		if err != nil || string(got) != body || r.Header.Get("Content-Encoding") != "" {
			http.Error(w, "model body changed", 500)
			return
		}
		_, _ = io.WriteString(w, "kept")
	}))
	// A rejected body must not consume the tools-once slot as a host grant.
	refused, _ := f.request(t, "POST", "api.openai.com", "/v1/responses", []byte(`{"tools":[{"type":"mcp"}],"input":[{"type":"input_file","file_url":"https://example.com"}]}`), "")
	if refused.Code != 403 || f.dials.Load() != 0 {
		t.Fatal("refused body spent or dialled its conditional net allowance")
	}
	var compressed bytes.Buffer
	zip := gzip.NewWriter(&compressed)
	_, _ = io.WriteString(zip, body)
	_ = zip.Close()
	response, audit := f.request(t, "POST", "api.openai.com", "/v1/responses", compressed.Bytes(), "gzip")
	if response.Code != 200 || response.Body.String() != "kept" || audit.Decision != "allow" || f.requests.Load() != 1 {
		t.Fatalf("scoped model grant: %d %s %+v", response.Code, response.Body.String(), audit)
	}
	second, _ := f.request(t, "POST", "api.openai.com", "/v1/responses", []byte(body), "")
	if second.Code != 403 || f.requests.Load() != 1 {
		t.Fatal("another request reused a spent model capability flow")
	}
}

func TestModelHTTPFileUploadRequiresFilesAndDoesNotOpenOtherPaths(t *testing.T) {
	grant := api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "api.openai.com", Tools: []string{"files"}}
	f := newModelHTTPFixture(t, "shell", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{grant}}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "authored file bytes" || r.Method != "POST" || r.URL.Path != "/v1/files" {
			http.Error(w, "file upload changed", 500)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	response, _ := f.request(t, "POST", "api.openai.com", "/v1/files", []byte("authored file bytes"), "")
	if response.Code != http.StatusCreated || f.requests.Load() != 1 {
		t.Fatalf("files grant did not allow its raw upload: %d", response.Code)
	}
	for _, path := range []string{"/v1/assistants", "/v1/messages/batches", "/v1/files/file_1"} {
		refused, _ := f.request(t, "POST", "api.openai.com", path, []byte(`{}`), "")
		if refused.Code != http.StatusForbidden || refused.Header().Get("X-Should-Retry") != "false" || f.requests.Load() != 1 {
			t.Fatalf("files capability opened another path %q", path)
		}
	}
}
