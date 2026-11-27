package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/link"
)

var traversalPaths = []string{
	"/copilot_internal/../repos/o/r",
	"/copilot_internal/./v2",
	"/copilot_internal/..",
	"/copilot_internal/%2e%2e/repos/o/r",
	"/copilot_internal/.%2e/repos/o/r",
	"/copilot_internal/%2E./repos/o/r",
	"/copilot_internal/%2e%2E%2Frepos/o/r",
	"/copilot_internal/%2f../repos/o/r",
	"/copilot_internal%2f..%2frepos/o/r",
	"/copilot_internal/%252e%252e/repos/o/r",
	"/copilot_internal/%25252e%25252e/repos/o/r",
	"/copilot_internal/%255c..%255crepos/o/r",
	"/copilot_internal/..\\repos/o/r",
	"/copilot_internal/%5c..%5crepos/o/r",
	"/copilot_internal//../repos/o/r",
}

func TestDecideRejectsPathTraversal(t *testing.T) {
	for _, preset := range []api.Preset{api.PresetLocked, api.PresetOpen} {
		eng, err := NewEngine("shell", pathTestPolicy(t, preset, "api.example.com/copilot_internal/"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range traversalPaths {
			t.Run(string(preset)+path, func(t *testing.T) {
				got := eng.Decide(Request{Host: "api.example.com", Port: 443, Method: "GET", Path: path})
				if got.Allow {
					t.Fatalf("traversal %q allowed by %s", path, got.Rule)
				}
			})
		}
	}
}

func TestPathRulesKeepExactAndPrefixBoundaries(t *testing.T) {
	for _, tc := range []struct {
		rule, path string
		allow      bool
	}{
		{"/user", "/user", true},
		{"/user", "/users", false},
		{"/user", "/user/repos", false},
		{"/login/", "/login/", true},
		{"/login/", "/login/oauth", true},
		{"/login/", "/login", false},
		{"/login/", "/login-evil", false},
		{"/copilot_internal/", "/copilot_internal/v2", true},
		{"/copilot_internal/", "/copilot_internal2/v2", false},
	} {
		t.Run(tc.rule+tc.path, func(t *testing.T) {
			eng, err := NewEngine("shell", pathTestPolicy(t, api.PresetLocked, "api.example.com"+tc.rule))
			if err != nil {
				t.Fatal(err)
			}
			got := eng.Decide(Request{Host: "api.example.com", Port: 443, Path: tc.path})
			if got.Allow != tc.allow {
				t.Fatalf("Decide(%q) = %+v, want allow=%v", tc.path, got, tc.allow)
			}
			for _, method := range []string{"CONNECT", "TCP"} {
				if got := eng.Decide(Request{Host: "api.example.com", Port: 443, Method: method}); got.Allow {
					t.Fatalf("a path grant allowed %s without a visible path", method)
				}
			}
		})
	}
}

func TestInvalidPathsDoNotConsumeAOnceGrant(t *testing.T) {
	for _, path := range []string{
		"/copilot_internal/%", "/copilot_internal/%2", "/copilot_internal/%gg",
		"/copilot_internal/%00v2", "/copilot_internal/%1fv2", "/copilot_internal/%7fv2",
		"/copilot_internal/%252e%252e/repos/o/r", "/copilot_internal/%252f../repos/o/r",
	} {
		t.Run(path, func(t *testing.T) {
			pol := pathTestPolicy(t, api.PresetLocked, "api.example.com/copilot_internal/")
			pol.Rules[0].Once = true
			eng, err := NewEngine("shell", pol)
			if err != nil {
				t.Fatal(err)
			}
			if got := eng.Decide(Request{Host: "api.example.com", Port: 443, Path: path}); got.Allow {
				t.Fatalf("invalid path was allowed: %+v", got)
			}
			valid := Request{Host: "api.example.com", Port: 443, Path: "/copilot_internal/v2"}
			if !eng.Decide(valid).Allow || eng.Decide(valid).Allow {
				t.Fatal("an invalid path consumed the once grant, or the valid path used it twice")
			}
		})
	}
}

func TestRuleTargetsRequireCanonicalPaths(t *testing.T) {
	for _, path := range traversalPaths {
		if got, err := ValidateRuleTarget("api.example.com" + path); err == nil {
			t.Errorf("ambiguous rule target was accepted as %q", got)
		}
	}
	for _, tc := range []struct{ raw, want string }{
		{"api.example.com/%75ser", "api.example.com/user"},
		{"api.example.com/caf%c3%a9/", "api.example.com/caf%C3%A9/"},
		{"api.example.com/name%20with%20spaces/", "api.example.com/name%20with%20spaces/"},
	} {
		got, err := ValidateRuleTarget(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("ValidateRuleTarget(%q) = %q, %v, want %q", tc.raw, got, err, tc.want)
		}
		if again, err := ValidateRuleTarget(got); err != nil || again != got {
			t.Errorf("canonical target changed on a second validation: %q, %v", again, err)
		}
	}
}

func TestPathScopedWildcardKeepsTheApexClosed(t *testing.T) {
	eng, err := NewEngine("shell", pathTestPolicy(t, api.PresetLocked, "*.example.com/copilot_internal/"))
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"api.example.com", "api.sub.example.com", "example.com", "otherexample.com"} {
		got := eng.Decide(Request{Host: host, Port: 443, Path: "/copilot_internal/v2"})
		want := host == "api.example.com" || host == "api.sub.example.com"
		if got.Allow != want {
			t.Errorf("wildcard decision for %s = %+v, want allow=%v", host, got, want)
		}
	}
}

type upstreamPath struct {
	uri, path, query string
}

func TestPlainHTTPRejectsPathTraversalBeforeDial(t *testing.T) {
	for _, path := range traversalPaths {
		t.Run(path, func(t *testing.T) {
			env, _, dials := startPathHTTP(t, "/copilot_internal/")
			if status := pathHTTPGet(t, env, path); status != http.StatusForbidden {
				t.Fatalf("traversal returned %d, want 403", status)
			}
			if dials.Load() != 0 {
				t.Fatal("an ambiguous path reached the upstream dialler")
			}
		})
	}
}

func TestPlainHTTPForwardsTheCanonicalPolicyPath(t *testing.T) {
	for _, tc := range []struct {
		rule, target, path, query, uri string
	}{
		{"/copilot_internal/", "/copilot_internal/%76%32?x=%2f%252e", "/copilot_internal/v2", "x=%2f%252e", "/copilot_internal/v2?x=%2f%252e"},
		{"/user", "/%75ser", "/user", "", "/user"},
		{"/my%20file", "/my%20file", "/my file", "", "/my%20file"},
		{"/caf%C3%A9", "/caf%c3%a9", "/café", "", "/caf%C3%A9"},
		{"/café/", "/caf%C3%A9/%76%32", "/café/v2", "", "/caf%C3%A9/v2"},
		{"/login%2F", "/login%2Foauth", "/login/oauth", "", "/login/oauth"},
		{"/", "", "/", "", "/"},
		{"/", "?x=1", "/", "x=1", "/?x=1"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			env, seen, dials := startPathHTTP(t, tc.rule)
			if status := pathHTTPGet(t, env, tc.target); status != http.StatusOK {
				t.Fatalf("canonical path returned %d, want 200", status)
			}
			select {
			case got := <-seen:
				if got.path != tc.path || got.query != tc.query || got.uri != tc.uri {
					t.Fatalf("upstream received %+v, want path=%q query=%q uri=%q", got, tc.path, tc.query, tc.uri)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the allowed request did not reach upstream")
			}
			if dials.Load() != 1 {
				t.Fatalf("upstream dials = %d, want 1", dials.Load())
			}
		})
	}
}

func pathTestPolicy(t *testing.T, preset api.Preset, targets ...string) api.Policy {
	t.Helper()
	var rules []api.Rule
	for _, target := range targets {
		rules = append(rules, api.Rule{Door: api.DoorNet, Allow: true, Target: target})
	}
	pol, _, err := MergeLayers([]api.Layer{{Kind: api.LayerSession, Preset: preset, Rules: rules}})
	if err != nil {
		t.Fatal(err)
	}
	return pol
}

func startPathHTTP(t *testing.T, rule string) (*testEnv, <-chan upstreamPath, *atomic.Int32) {
	t.Helper()
	seen := make(chan upstreamPath, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- upstreamPath{uri: r.RequestURI, path: r.URL.Path, query: r.URL.RawQuery}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(ts.Close)
	dials := &atomic.Int32{}
	d := &Dialer{
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
		},
		DialTCP: func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
			dials.Add(1)
			var nd net.Dialer
			return nd.DialContext(ctx, "tcp", ts.Listener.Addr().String())
		},
	}
	env := startTestServer(t, d)
	env.call(t, CtlInit, InitArgs{
		SessionID: "paths2345", SessionRef: "path-box", Agent: "shell",
		Policy: pathTestPolicy(t, api.PresetLocked, "example.com:80"+rule),
	})
	return env, seen, dials
}

func pathHTTPGet(t *testing.T, env *testEnv, target string) int {
	t.Helper()
	return pathHTTPExchange(t, env, fmt.Sprintf("GET http://example.com%s HTTP/1.1\r\nHost: example.com\r\n\r\n", target))
}

func pathHTTPExchange(t *testing.T, env *testEnv, request string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := env.supSess.Open(ctx, link.KindEgress, StreamHeader{Via: "explicit", Host: "example.com", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(st, request); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(st), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}
