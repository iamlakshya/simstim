package egress

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

type serviceMintHTTPFixture struct {
	control                  *boundControlFixture
	lookups, dials, requests atomic.Int64
}

func newServiceMintHTTPFixture(t *testing.T, host string, bound bool, policy api.Policy, handler http.Handler) *serviceMintHTTPFixture {
	t.Helper()
	f := new(serviceMintHTTPFixture)
	testCtx := t.Context()
	dialer := &Dialer{Lookup: func(context.Context, string) ([]netip.Addr, error) {
		f.lookups.Add(1)
		return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
	}, DialTCP: func(_ context.Context, address netip.AddrPort) (net.Conn, error) {
		if address != netip.MustParseAddrPort("198.51.100.8:443") {
			return nil, errors.New("unchecked fixture endpoint")
		}
		f.dials.Add(1)
		client, origin := net.Pipe()
		t.Cleanup(func() { _ = client.Close(); _ = origin.Close() })
		go serveHTTPConnection(testCtx, origin, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			f.requests.Add(1)
			_, _ = io.Copy(io.Discard, r.Body)
			handler.ServeHTTP(w, r)
		}), nil)
		return client, nil
	}}
	f.control = newBoundControlFixtureWithOptions(t, true, Options{Dialer: dialer})
	values := map[string]string{}
	if bound {
		values["KEY"] = "AUTHORED00000000"
	}
	args := boundRegistryArgs(values)
	args.Initial.Policy = policy
	if bound {
		args.Binds["KEY"] = api.SecretBind{Hosts: []string{host}, Header: "Authorization", Format: "Bearer {value}"}
	}
	if _, err := f.control.server.initialiseBoundControl(memberJSON(t, args)); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *serviceMintHTTPFixture) request(t *testing.T, method, host, path string) (*httptest.ResponseRecorder, AuditEntry) {
	t.Helper()
	hdr := StreamHeader{Via: "explicit", Host: host, Port: 443}
	conn, peer := net.Pipe()
	defer peer.Close()
	ctx, finish, err := f.control.server.beginSecretCarrier(t.Context(), conn, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	req := httptest.NewRequest(method, "http://"+host+":443"+path, strings.NewReader(`{}`)).WithContext(ctx)
	w := httptest.NewRecorder()
	audit := f.control.server.proxyHTTPRequest(w, req, hdr, "http", nil)
	return w, audit.get()
}

func TestBoundMintEndpointDataCoversExactListedMethodsAndPaths(t *testing.T) {
	for _, tc := range []struct{ host, method, path string }{
		{"api.github.com", "POST", "/app/installations/1/access_tokens"},
		{"api.github.com", "POST", "/app-manifests/code/conversions"},
		{"api.github.com", "POST", "/applications/client/token/scoped"},
		{"api.github.com", "PATCH", "/applications/client/token"},
		{"github.com", "POST", "/login/oauth/access_token"},
		{"api.openai.com", "POST", "/v1/organization/admin_api_keys"},
		{"api.openai.com", "POST", "/v1/organization/projects/proj/service_accounts"},
		{"api.openai.com", "POST", "/v1/organization/projects/proj/service_accounts/account/api_keys"},
		{"gitlab.com", "POST", "/api/v4/personal_access_tokens/self/rotate"},
		{"gitlab.com", "POST", "/api/v4/projects/1/access_tokens"},
		{"gitlab.com", "POST", "/api/v4/projects/1/access_tokens/self/rotate"},
		{"gitlab.com", "POST", "/api/v4/groups/1/access_tokens"},
		{"gitlab.com", "POST", "/api/v4/groups/1/access_tokens/self/rotate"},
		{"gitlab.com", "POST", "/api/v4/user/personal_access_tokens"},
		{"gitlab.com", "POST", "/api/v4/users/1/personal_access_tokens"},
		{"gitlab.com", "POST", "/api/v4/users/1/impersonation_tokens"},
	} {
		request := Request{Host: tc.host, Port: 443, Method: tc.method, Path: tc.path}
		rules, err := serviceBarriersFor(request)
		found := false
		for _, rule := range rules {
			found = found || rule.BoundOnly && rule.Kind == "grant"
		}
		if err != nil || !found {
			t.Fatalf("missing documented mint endpoint: %s %s", tc.method, tc.path)
		}
		request.Method = "GET"
		rules, err = serviceBarriersFor(request)
		if err != nil {
			t.Fatal(err)
		}
		for _, rule := range rules {
			if rule.BoundOnly {
				t.Fatal("mint row silently widened to reads")
			}
		}
	}
}

func TestBoundMintAnonymousHTTPNeedsEndpointAuthorityBeforeLeaseOrOrigin(t *testing.T) {
	const host = "api.github.com"
	const path = "/app/installations/1/access_tokens"
	for _, bound := range []bool{false, true} {
		for _, explicit := range []bool{false, true} {
			policy := api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: host}}}
			if explicit {
				policy.Rules = append(policy.Rules, api.Rule{Layer: api.LayerSession, Allow: true, Target: host + path, Once: true, GrantID: "mint-endpoint"})
			} // Legacy nilMethods remains deliberate.
			f := newServiceMintHTTPFixture(t, host, bound, policy, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ordinary") }))
			engine := f.control.server.currentEngine()
			before := snapshotDNSLedger(engine)
			response, audit := f.request(t, "POST", host, path)
			if bound && !explicit {
				if response.Code != 403 || f.lookups.Load() != 0 || f.dials.Load() != 0 || f.requests.Load() != 0 || !strings.HasPrefix(audit.Rule, "service:") || !reflect.DeepEqual(before, snapshotDNSLedger(engine)) {
					t.Fatal("bound anonymous mint used broad networking, spent a lease or reached an origin")
				}
			} else if response.Code != 200 || f.requests.Load() != 1 {
				t.Fatal("unbound ordinary policy or explicit endpoint contract changed")
			}
			if bound && explicit {
				second, _ := f.request(t, "POST", host, path)
				if second.Code != 403 || f.requests.Load() != 1 {
					t.Fatal("spent mint endpoint lease fell back to broad networking")
				}
			}
		}
	}
}

func TestBoundMintRevalidationUsesCurrentExactAuthorityAndRetainedPath(t *testing.T) {
	const host = "api.github.com"
	const path = "/app/installations/1/access_tokens"
	policy := api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: host}}}
	bound := newServiceMintHTTPFixture(t, host, true, policy, http.NotFoundHandler()).control.server.currentMembers()
	unbound := newServiceMintHTTPFixture(t, host, false, policy, http.NotFoundHandler()).control.server.currentMembers()
	engine, err := NewEngine("shell", policy)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Host: host, Port: 443, Method: "POST", Path: path}
	permit := engine.NewPermit()
	defer permit.Close()
	if !engine.admitServiceRequest(request, permit, false, evalReserve).Allow {
		t.Fatal("fixture networking refused")
	}
	permit.Commit()
	service := &serviceRequest{request: request, boundMint: true}
	if !service.allowedAtState(engine, permit, unbound) || service.allowedAtState(engine, permit, bound) || service.allowed(engine, permit) {
		t.Fatal("bound authority was cached, guessed or lost on revalidation")
	}
	retired := &memberState{version: MemberRegistryV2, retired: map[string][]registeredRetired{"KEY": {{binding: bound.secrets["KEY"].bound.binding}}}}
	if !service.requiresPath(retired) {
		t.Fatal("retired exact binding lost its mint endpoint protection")
	}
	service.request.Port = 80
	if service.requiresPath(bound) {
		t.Fatal("HTTPS443 binding donated authority to port80")
	}
	service.request.Port = 443
	service.request.Host = "other.github.com"
	if service.requiresPath(bound) {
		t.Fatal("binding donated authority to another host")
	}
	service.request = request
	service.pathGrant = true
	if service.allowedAtState(engine, permit, unbound) {
		t.Fatal("removed binding dropped a path requirement already owned by this request")
	}
}

func TestBoundMintServiceTokenGzipResponseUsesActualDurableRegistry(t *testing.T) {
	value := "ghp_" + strings.Repeat("A", 36)
	var compressed bytes.Buffer
	zip := gzip.NewWriter(&compressed)
	_, _ = io.WriteString(zip, `{"token":"`+value+`"}`)
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ host, method, path string }{{"api.github.com", "GET", "/authored-response"}, {"api.openai.com", "POST", "/v1/responses"}} {
		t.Run(tc.host, func(t *testing.T) {
			policy := api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: tc.host}}}
			f := newServiceMintHTTPFixture(t, tc.host, true, policy, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("origin request did not request identity encoding")
				}
				w.Header().Set("Content-Encoding", "gzip")
				_, _ = w.Write(compressed.Bytes())
			}))
			response, _ := f.request(t, tc.method, tc.host, tc.path)
			if response.Code != 200 || response.Header().Get("Content-Encoding") != "" || strings.Contains(response.Body.String(), value) {
				t.Fatal("compressed service credential escaped the actual HTTP response engine")
			}
			state := f.control.server.currentMembers()
			owner := state.secrets["KEY"]
			if len(owner.minted) != 1 || !owner.minted[0].ready || owner.minted[0].rule != "github-pat" || owner.minted[0].authority != tc.host+":443" || !strings.Contains(response.Body.String(), string(owner.minted[0].sentinel)) || f.control.eventCount.Load() != 1 {
				t.Fatal("HTTP mint did not join its same bound owner, one event and durable ACK registry")
			}
		})
	}
}

func TestBoundMintEndpointNormalMethodsLocksDeniesAndExpiryRemainFinal(t *testing.T) {
	const host = "api.github.com"
	const path = "/app/installations/1/access_tokens"
	for _, failure := range []string{"method", "deny", "lock", "expired"} {
		t.Run(failure, func(t *testing.T) {
			pathRule := api.Rule{Layer: api.LayerSession, Allow: true, Target: host + path, Once: true, GrantID: "mint-endpoint"}
			policy := api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{{Layer: api.LayerSession, Allow: true, Target: host}, pathRule}}
			switch failure {
			case "method":
				policy.Rules[1].Methods = new([]string{"PUT"})
			case "deny":
				policy.Rules = append(policy.Rules, api.Rule{Layer: api.LayerSession, Target: host + path})
			case "lock":
				policy.Locks = []api.Lock{{Layer: api.LayerAll, Deny: []string{host + path}}}
			case "expired":
				policy.Rules[1].Until = time.Now().Add(-time.Minute)
			}
			f := newServiceMintHTTPFixture(t, host, true, policy, http.NotFoundHandler())
			engine := f.control.server.currentEngine()
			before := snapshotDNSLedger(engine)
			response, _ := f.request(t, "POST", host, path)
			if response.Code != 403 || f.lookups.Load() != 0 || f.dials.Load() != 0 || f.requests.Load() != 0 || !reflect.DeepEqual(before, snapshotDNSLedger(engine)) {
				t.Fatal("mint endpoint bypassed a normal restriction or mutated a refused lease")
			}
		})
	}
}
