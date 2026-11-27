package egress

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestCanonicalPathRejectsAmbiguity(t *testing.T) {
	for _, path := range []string{
		"//", "/%2F", "/%2f", "/%FF", "/%C0%AF", "%2Fuser", "user",
		"/x%3fy", "/x%23y", "/x%25y", "/a//b", "/a/%2e/b", "/a/%2e%2e/b",
	} {
		if got, err := canonicalPath(path); err == nil {
			t.Errorf("ambiguous path %q was accepted as %q", path, got)
		}
	}
	for _, tc := range []struct{ raw, want string }{
		{"", ""}, {"/", "/"}, {"/%75ser", "/user"},
		{"/my%20file", "/my file"}, {"/my file", "/my file"},
		{"/caf%C3%A9", "/café"}, {"/café", "/café"},
		{"/a+b", "/a+b"}, {"/login%2F", "/login/"},
	} {
		got, err := canonicalPath(tc.raw)
		if err != nil || got != tc.want {
			t.Errorf("canonicalPath(%q) = %q, %v, want %q", tc.raw, got, err, tc.want)
		}
		if again, err := canonicalPath(got); err != nil || again != got {
			t.Errorf("canonical path was not idempotent: %q, %v", again, err)
		}
	}
}

func TestPathPrefixUsesTheCanonicalTarget(t *testing.T) {
	cr, err := compileTarget("example.com/login%2F", true)
	if err != nil || cr.path != "/login/" || !cr.pathPrefix {
		t.Fatalf("compiled encoded slash = %+v, %v", cr, err)
	}
	eng, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{
		{Layer: api.LayerSession, Door: api.DoorNet, Allow: true, Target: "example.com/login%2F"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !eng.Decide(Request{Host: "example.com", Port: 443, Path: "/login/oauth"}).Allow {
		t.Fatal("the canonical prefix did not allow its descendant")
	}
}

func TestCanonicalPathUsedByAllPolicySources(t *testing.T) {
	for _, source := range []string{"lock", "layer", "agent", "preset"} {
		t.Run(source, func(t *testing.T) {
			preset := api.PresetLocked
			switch source {
			case "lock":
				preset = api.PresetOpen
			case "preset":
				preset = api.PresetBalanced
			}
			eng, err := NewEngine("shell", api.Policy{Preset: preset})
			if err != nil {
				t.Fatal(err)
			}
			rule, err := compileTarget("example.com/user", source != "lock")
			if err != nil {
				t.Fatal(err)
			}
			switch source {
			case "lock":
				eng.locks = []compiledRule{rule}
			case "layer":
				eng.layers = []compiledLayer{{kind: api.LayerSession, rules: []compiledRule{rule}}}
			case "agent":
				eng.agentRules = []compiledRule{rule}
			case "preset":
				eng.balanced = []compiledRule{rule}
			}
			for _, path := range []string{"/user", "/%75ser", "/users"} {
				want := path != "/users"
				if source == "lock" {
					want = !want
				}
				got := eng.Decide(Request{Host: "example.com", Port: 443, Path: path})
				if got.Allow != want {
					t.Errorf("%s decision for %q = %+v, want allow=%v", source, path, got, want)
				}
			}
		})
	}
}

func TestCanonicalPathsSurviveFlowRevalidation(t *testing.T) {
	for _, path := range []string{"/my%20file", "/caf%C3%A9"} {
		t.Run(path, func(t *testing.T) {
			pol := pathTestPolicy(t, api.PresetLocked, "example.com"+path)
			eng, err := NewEngine("shell", pol)
			if err != nil {
				t.Fatal(err)
			}
			clean, err := canonicalPath(path)
			if err != nil || !eng.Decide(Request{Host: "example.com", Port: 443, Path: path}).Allow {
				t.Fatalf("canonical path was not allowed: %v", err)
			}
			ft := newFlowTracker(1)
			closed := false
			permit := eng.NewPermit()
			request := Request{Host: "example.com", Port: 443, Path: clean}
			if !eng.DecideWithPermit(request, permit).Allow {
				t.Fatal("the test flow was not authorised")
			}
			ft.add(activeFlow{request: request, permit: permit, cancel: func() { closed = true }})
			ft.revalidate(eng, &Dialer{})
			if closed {
				t.Fatal("revalidation closed a canonical path allowed by the same policy")
			}
			denied, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked})
			if err != nil {
				t.Fatal(err)
			}
			ft.revalidate(denied, &Dialer{})
			if !closed {
				t.Fatal("revocation left the canonical flow open")
			}
		})
	}
}

func TestPlainHTTPTargetRejectsUserInformationAndFragments(t *testing.T) {
	for _, target := range []string{
		"http://user@example.com/ok", "http://example.com/ok?x=1#fragment", "http://example.com/ok#fragment",
	} {
		t.Run(target, func(t *testing.T) {
			env, _, dials := startPathHTTP(t, "/")
			status := pathHTTPExchange(t, env, "GET "+target+" HTTP/1.1\r\nHost: example.com\r\n\r\n")
			if status != http.StatusBadRequest || dials.Load() != 0 {
				t.Fatalf("invalid target returned %d after %d upstream dials", status, dials.Load())
			}
		})
	}
	for _, u := range []*url.URL{
		{Scheme: "http", Host: "example.com", Opaque: "//example.com/ok"},
		{Scheme: "http", Host: "example.com", Path: "/ok", User: url.User("user")},
	} {
		if _, _, err := extractRequestTarget(&http.Request{URL: u, RequestURI: "http://example.com/ok", Method: http.MethodGet}); err == nil {
			t.Errorf("invalid parsed target was accepted: %+v", u)
		}
	}
}

func TestPlainHTTPRejectsInvalidPathEncodingBeforeDial(t *testing.T) {
	for _, path := range []string{"//", "/%2F", "/%FF", "/%C0%AF"} {
		t.Run(path, func(t *testing.T) {
			env, _, dials := startPathHTTP(t, "/")
			if status := pathHTTPGet(t, env, path); status != http.StatusForbidden || dials.Load() != 0 {
				t.Fatalf("invalid path returned %d after %d upstream dials", status, dials.Load())
			}
		})
	}
}

func FuzzCanonicalPath(f *testing.F) {
	for _, path := range []string{"", "/", "/user", "/my%20file", "/café/", "/%2F", "/%FF", "/a/%252e%252e/b"} {
		f.Add(path)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		clean, err := canonicalPath(raw)
		if err != nil {
			return
		}
		if again, err := canonicalPath(clean); err != nil || again != clean {
			t.Fatalf("canonical path changed on revalidation: %q to %q, %v", clean, again, err)
		}
		escaped := (&url.URL{Path: clean}).EscapedPath()
		if again, err := canonicalPath(escaped); err != nil || again != clean {
			t.Fatalf("canonical path changed on forwarding: %q to %q, %v", clean, again, err)
		}
	})
}
