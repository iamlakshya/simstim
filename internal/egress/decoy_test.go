package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

func decoyTestHome(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS", "XDG_DATA_DIRS"} {
		path := filepath.Join(root, key)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
}

func decoyTestGeneration(t *testing.T) []api.DecoyTripwire {
	t.Helper()
	definitions, err := proto.DecoyDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	var output []api.DecoyTripwire
	for _, definition := range definitions {
		tripwire := api.DecoyTripwire{Name: definition.Name, Hosts: append([]string(nil), definition.Hosts...)}
		for _, format := range definition.Values {
			tripwire.Values = append(tripwire.Values, api.Secret(format.Prefix+strings.Repeat(format.Alphabet[:1], format.Length-len(format.Prefix))))
		}
		output = append(output, tripwire)
	}
	return output
}

func newDecoyHTTPFixture(t *testing.T, policy api.Policy, generation []api.DecoyTripwire, handler http.Handler) *modelHTTPFixture {
	t.Helper()
	decoyTestHome(t)
	f := new(modelHTTPFixture)
	dialer := &Dialer{Lookup: func(context.Context, string) ([]netip.Addr, error) {
		f.lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
	}, DialTCP: func(_ context.Context, address netip.AddrPort) (net.Conn, error) {
		if address != netip.MustParseAddrPort("198.51.100.8:443") {
			return nil, fmt.Errorf("unchecked decoy fixture address")
		}
		f.dials.Add(1)
		client, origin := net.Pipe()
		t.Cleanup(func() { _ = client.Close(); _ = origin.Close() })
		go serveHTTPConnection(t.Context(), origin, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.requests.Add(1); handler.ServeHTTP(w, r) }), nil)
		return client, nil
	}}
	f.env = startTestServer(t, dialer)
	f.env.call(t, CtlInit, InitArgs{SessionID: "decoy234567", Agent: "shell", DNS: DNSModeReal, Policy: policy, Decoys: generation})
	return f
}

type decoyLedgerProbeBody struct {
	t      *testing.T
	engine *Engine
	reader io.Reader
}

func (b *decoyLedgerProbeBody) Read(p []byte) (int, error) {
	if !b.engine.ledger.mu.TryLock() {
		b.t.Error("body inspection began while ledger ownership was held")
	} else {
		for _, slot := range b.engine.ledger.slots {
			if slot.state != slotAvailable {
				b.t.Error("body inspection began after a once slot was reserved")
			}
		}
		b.engine.ledger.mu.Unlock()
	}
	return b.reader.Read(p)
}
func (*decoyLedgerProbeBody) Close() error { return nil }

func TestDecoyBase64FormsRetainEveryValueBitAtAllThreeAlignments(t *testing.T) {
	decoyTestHome(t)
	value := api.Secret("AbCd0123456789+/_-Token")
	forms := decoyValueForms(value)
	for alignment := 0; alignment < 3; alignment++ {
		for _, before := range []byte{0, 1, 127, 255} {
			for _, after := range []byte{0, 1, 127, 255} {
				input := append(bytes.Repeat([]byte{before}, alignment), []byte(value)...)
				input = append(input, after, after)
				encoded := base64.StdEncoding.EncodeToString(input)
				found := false
				for _, form := range forms {
					found = found || strings.Contains(encoded, string(form))
				}
				if !found {
					t.Fatal("an embedded standard base64 alignment escaped the existing matcher inputs")
				}
			}
		}
	}
	changed := []byte(value)
	changed[len(changed)/2] = '!'
	for alignment := 0; alignment < 3; alignment++ {
		input := append(bytes.Repeat([]byte{0}, alignment), changed...)
		input = append(input, 0, 0)
		encoded := base64.StdEncoding.EncodeToString(input)
		for _, form := range forms {
			if strings.Contains(encoded, string(form)) {
				t.Fatal("a changed value matched a base64 pattern that discarded value bits")
			}
		}
	}
}

func TestDecoyURLHeaderDNSAndBodyUseOnePrePolicyTripwireAndLabel(t *testing.T) {
	generation := decoyTestGeneration(t)
	for _, tripwire := range generation {
		for _, value := range tripwire.Values {
			for _, kind := range []string{"url", "header", "basic", "body", "gzip body", "base64 body", "dns"} {
				t.Run(tripwire.Name+"/"+kind+fmt.Sprint(len(value)), func(t *testing.T) {
					policy := onceTestPolicy(t, api.PresetOpen, api.Rule{Layer: api.LayerSession, Allow: true, Target: "example.com", Once: true, GrantID: "untouched"})
					f := newDecoyHTTPFixture(t, policy, generation, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
					before := snapshotDNSLedger(f.env.srv.currentEngine())
					var audit AuditEntry
					if kind == "dns" {
						packet := buildSingleDNSQuery(t, 0x7142, true, string(value)+".example.com.", dnsmessage.TypeA, dnsmessage.ClassINET)
						response := unpackDNSReply(t, f.env.srv.AnswerDNS(t.Context(), packet))
						if response.Header.RCode != dnsmessage.RCodeRefused || len(response.Answers) != 0 {
							t.Fatal("raw DNS decoy was not refused")
						}
						audit = f.env.waitAudits(t, 1)[0]
					} else {
						path, body := "/probe", []byte("ordinary body")
						req := httptest.NewRequest("POST", "http://example.com:443"+path, bytes.NewReader(body)).WithContext(t.Context())
						switch kind {
						case "url":
							req = httptest.NewRequest("GET", "http://example.com:443/probe?token="+string(value), nil).WithContext(t.Context())
						case "header":
							req.Header.Set("X-Probe", string(value))
						case "basic":
							req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:"+string(value))))
						case "body":
							body = []byte("prefix:" + string(value) + ":suffix")
							req = httptest.NewRequest("POST", "http://example.com:443/probe", bytes.NewReader(body)).WithContext(t.Context())
						case "gzip body":
							body = encodedBodyFixture(t, "gzip", strings.NewReader("prefix:"+string(value)+":suffix"))
							req = httptest.NewRequest("POST", "http://example.com:443/probe", bytes.NewReader(body)).WithContext(t.Context())
							req.Header.Set("Content-Encoding", "gzip")
						case "base64 body":
							body = []byte(base64.StdEncoding.EncodeToString([]byte("xx" + string(value) + "suffix")))
							req = httptest.NewRequest("POST", "http://example.com:443/probe", bytes.NewReader(body)).WithContext(t.Context())
						}
						if kind == "body" || kind == "gzip body" || kind == "base64 body" {
							req.Body = &decoyLedgerProbeBody{t: t, engine: f.env.srv.currentEngine(), reader: bytes.NewReader(body)}
						}
						writer := httptest.NewRecorder()
						result := f.env.srv.proxyHTTPRequest(writer, req, StreamHeader{Via: "explicit", Host: "example.com", Port: 443}, "http", nil)
						audit = result.get()
						if writer.Code != http.StatusForbidden {
							t.Fatal("observable decoy did not refuse the actual HTTP request")
						}
					}
					if audit.Rule != "decoy" || audit.Secret != tripwire.Name || audit.Decision != "deny" || f.lookups.Load() != 0 || f.dials.Load() != 0 || f.requests.Load() != 0 {
						t.Fatal("decoy label or pre-origin refusal was lost")
					}
					if !reflect.DeepEqual(before, snapshotDNSLedger(f.env.srv.currentEngine())) {
						t.Fatal("decoy refusal spent or transiently reserved an ordinary once grant")
					}
					encoded, _ := json.Marshal(audit)
					for _, candidate := range tripwire.Values {
						if bytes.Contains(encoded, []byte(candidate)) {
							t.Fatal("a fake value escaped into persistent audit metadata")
						}
					}
				})
			}
		}
	}
}

func TestDecoyBodyPrefixIsExactBoundedAndReplaysOriginalBytes(t *testing.T) {
	generation := decoyTestGeneration(t)
	value := generation[0].Values[0]
	for _, inside := range []bool{true, false} {
		t.Run(fmt.Sprint(inside), func(t *testing.T) {
			var body []byte
			if inside {
				body = append(bytes.Repeat([]byte{'q'}, decoyRequestBodyBytes-len(value)), []byte(value)...)
			} else {
				body = append(bytes.Repeat([]byte{'q'}, decoyRequestBodyBytes), []byte(value)...)
			}
			f := newDecoyHTTPFixture(t, onceTestPolicy(t, api.PresetOpen), generation, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				actual, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Equal(actual, body) {
					http.Error(w, "body changed", 500)
					return
				}
				w.WriteHeader(200)
			}))
			response, audit := f.request(t, "POST", "example.com", "/probe", body, "")
			if inside {
				if response.Code != 403 || audit.Rule != "decoy" || f.requests.Load() != 0 {
					t.Fatal("the end of the first body window escaped inspection")
				}
			} else if response.Code != 200 || f.requests.Load() != 1 {
				t.Fatal("the explicit body boundary consumed or changed the uninspected tail")
			}
		})
	}
}

type decoyBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *decoyBufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func TestDeniedDecoyHostStillReadsItsHeadersWithoutAnyOriginPermission(t *testing.T) {
	generation := decoyTestGeneration(t)
	var value api.Secret
	for _, tripwire := range generation {
		if tripwire.Name == "aws" {
			value = tripwire.Values[0]
		}
	}
	if value == "" {
		t.Fatal("canonical AWS fixture is missing")
	}
	f := newDecoyHTTPFixture(t, onceTestPolicy(t, api.PresetLocked), generation, http.NotFoundHandler())
	dns := buildSingleDNSQuery(t, 0x7151, true, "sts.us-east-1.amazonaws.com.", dnsmessage.TypeA, dnsmessage.ClassINET)
	answer := unpackDNSReply(t, f.env.srv.AnswerDNS(t.Context(), dns))
	if answer.Header.RCode != dnsmessage.RCodeSuccess || len(answer.Answers) != 1 || f.lookups.Load() != 0 {
		t.Fatal("denied regional decoy DNS did not route only to local inspection")
	}
	if !f.env.srv.currentMembers().decoyHost("sts.us-east-1.amazonaws.com", 443) || f.env.srv.currentMembers().decoyHost("sts.region.extra.amazonaws.com", 443) {
		t.Fatal("the existing class matcher did not retain the exact whole-label wildcard")
	}
	stream, err := f.env.supSess.Open(t.Context(), link.KindEgress, StreamHeader{Via: "explicit", Host: "sts.amazonaws.com", Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(stream, "CONNECT sts.amazonaws.com:443 HTTP/1.1\r\nHost: sts.amazonaws.com:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	buffer := bufio.NewReader(stream)
	response, err := http.ReadResponse(buffer, &http.Request{Method: "CONNECT"})
	if err != nil || response.StatusCode != 200 {
		t.Fatal("denied decoy class did not enter local inspection", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(f.env.srv.ca.cert)
	client := tls.Client(&decoyBufferedConn{Conn: stream, reader: buffer}, &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: "sts.amazonaws.com"})
	if err := client.HandshakeContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(client, "GET /?Action=GetCallerIdentity HTTP/1.1\r\nHost: sts.amazonaws.com\r\nAuthorization: AWS4-HMAC-SHA256 Credential=%s/date/region/sts/aws4_request\r\nConnection: close\r\n\r\n", string(value)); err != nil {
		t.Fatal(err)
	}
	response, err = http.ReadResponse(bufio.NewReader(client), &http.Request{Method: "GET"})
	if err != nil || response.StatusCode != 403 {
		t.Fatal("a denied host hid the decoy behind a generic network refusal", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	_ = client.Close()
	audits := f.env.waitAudits(t, 1)
	found := false
	for _, audit := range audits {
		found = found || audit.Rule == "decoy" && audit.Secret == "aws" && audit.Host == "sts.amazonaws.com"
	}
	if !found || f.lookups.Load() != 0 || f.dials.Load() != 0 || f.requests.Load() != 0 {
		t.Fatal("local denied-host inspection gained an origin permission or lost its decoy detail")
	}
}

func TestDecoyValuesNeverEnterPublicBoundStateOrCredentialAuthority(t *testing.T) {
	decoyTestHome(t)
	generation := decoyTestGeneration(t)
	args := boundRegistryArgs(map[string]string{"KEY": "ABCD0000"})
	args.Initial.Decoys = generation
	server, _ := newBoundRegistryServer(t, args)
	state := server.currentMembers()
	if len(state.metadata.Decoys) != 0 || len(state.decoys) != len(generation) {
		t.Fatal("applied fake values were stored in public metadata or lost")
	}
	public, err := state.publicSessionProjection(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	for _, tripwire := range generation {
		for _, value := range tripwire.Values {
			if bytes.Contains(encoded, []byte(value)) {
				t.Fatal("a fake value entered public receipt state")
			}
		}
	}
	for _, entry := range state.requestView.entries {
		for _, tripwire := range generation {
			if entry.source.Name == tripwire.Name {
				t.Fatal("a decoy became an armable or injectable credential name")
			}
		}
	}
	clone, err := cloneMemberState(state)
	if err != nil {
		t.Fatal(err)
	}
	clone.decoys[0].Values[0] = "changed"
	if state.decoys[0].Values[0] == "changed" {
		t.Fatal("a future registry candidate mutated the applied decoy generation")
	}
}

func TestDecoyAppliedGenerationSurvivesLiveColdFlagChanges(t *testing.T) {
	generation := decoyTestGeneration(t)
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			policy := onceTestPolicy(t, api.PresetOpen)
			policy.Sandbox.Decoys = &enabled
			input := generation
			if !enabled {
				input = nil
			}
			f := newDecoyHTTPFixture(t, policy, input, http.NotFoundHandler())
			next := onceTestPolicy(t, api.PresetLocked)
			changed := !enabled
			next.Sandbox.Decoys = &changed
			f.env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: next})
			state := f.env.srv.currentMembers()
			if (len(state.decoys) != 0) != enabled || state.decoyHost("sts.amazonaws.com", 443) != enabled {
				t.Fatal("a live cold-setting update changed the applied trap generation")
			}
			if f.env.srv.currentEngine().Decide(Request{Host: "ungranted.example", Port: 443}).Allow {
				t.Fatal("retaining cold decoys prevented an immediate network policy update")
			}
			cold, err := prepareInitialState(InitArgs{SessionID: "cold234567", Agent: "shell", Policy: next, Decoys: generation})
			if err != nil {
				t.Fatal(err)
			}
			if (len(cold.state.decoys) != 0) != changed {
				t.Fatal("a new cold start failed to apply its own decoy choice")
			}
		})
	}
}
