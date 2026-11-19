package egress

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestClassifierFixedDomainsKeepLabelBoundariesAndActualRuleAuthority(t *testing.T) {
	data, err := loadFixedClassifier()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range data.Denies {
		host := strings.TrimPrefix(row.Host, "*.")
		t.Run(host, func(t *testing.T) {
			engine, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownClassify})
			if err != nil {
				t.Fatal(err)
			}
			before := snapshotDNSLedger(engine)
			for _, blocked := range []string{host, "authored." + host} {
				decision := engine.Decide(Request{Host: blocked, Port: 443})
				if decision.Allow || decision.Rule != "classify:fixed" || decision.unmatched {
					t.Fatal("fixed destination reached the next classifier stage")
				}
			}
			for _, near := range []string{"not-" + host, host + ".example"} {
				if got := engine.Decide(Request{Host: near, Port: 443}); got.Allow || got.Rule != "classify:unsure" || !got.unmatched {
					t.Fatal("fixed destination matched a different DNS label or domain")
				}
			}
			if !reflect.DeepEqual(before, snapshotDNSLedger(engine)) {
				t.Fatal("fixed classification changed the actual lease ledger")
			}
			granted, err := NewEngine("shell", api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownClassify, Rules: []api.Rule{{Layer: api.LayerSession, Target: host, Allow: true}}})
			if err != nil || !granted.Decide(Request{Host: host, Port: 443}).Allow {
				t.Fatal("a classifier for uncovered flows overrode an actual explicit grant", err)
			}
		})
	}
}

func TestClassifierFixedNumericAndCompleteLongLabelChecks(t *testing.T) {
	engine, err := NewEngine("shell", api.Policy{Preset: api.PresetOpen, Unknown: api.UnknownClassify})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"198.51.100.8", "2001:db8::8"} {
		if got := engine.Decide(Request{Host: host, Port: 443}); got.Allow || got.Rule != "classify:ip" {
			t.Fatal("uncovered literal IP reached a judge or the open preset")
		}
	}
	label := "0123456789abcdefghijklmnopqrstuvwxyzabcd"
	if len(label) != 40 {
		t.Fatal("random-label boundary fixture changed")
	}
	for _, host := range []string{label + ".example", "authored." + label + ".example"} {
		if got := engine.Decide(Request{Host: host, Port: 443}); got.Allow || got.Rule != "classify:random" {
			t.Fatal("complete 40-byte random-looking label escaped the fixed stage")
		}
	}
	for _, host := range []string{label[:39] + ".example", strings.Repeat("a", 63) + ".example", "authored.example"} {
		if got := engine.Decide(Request{Host: host, Port: 443}); got.Allow || got.Rule != "classify:unsure" {
			t.Fatal("length alone or an unsupported guess became a fixed deny")
		}
	}
	for _, host := range []string{"authored..example", "a\x00.example", strings.Repeat("x.", 126) + "example"} {
		if got := engine.Decide(Request{Host: host, Port: 443}); got.Allow || got.Rule != "invalid" {
			t.Fatal("normalisation did not run before the fixed stage")
		}
	}
}

func TestClassifierWebhookServiceBarriersUseTheActualAtomicEndpointPlan(t *testing.T) {
	for _, route := range []struct{ host, method, path string }{
		{"discord.com", "POST", "/api/webhooks/id/authored"},
		{"discord.com", "GET", "/api/webhooks/id/authored"},
		{"discordapp.com", "Authored", "/api/webhooks/id/authored"},
		{"api.telegram.org", "POST", "/bot123:authored/sendMessage"},
		{"api.telegram.org", "GET", "/bot123:authored/sendMessage"},
		{"pastebin.com", "POST", "/api/api_post.php"},
		{"pastebin.com", "Authored", "/authored"},
	} {
		for _, grant := range []string{"none", "wrong-method", "exact", "legacy"} {
			t.Run(route.host+route.method+grant, func(t *testing.T) {
				policy := api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownClassify, Rules: []api.Rule{{Layer: api.LayerSession, Target: route.host, Allow: true}}}
				if grant != "none" {
					rule := api.Rule{Layer: api.LayerSession, Target: route.host + route.path, Allow: true, Once: true, GrantID: "authored-endpoint"}
					if grant == "exact" {
						rule.Methods = new([]string{route.method})
					} else if grant == "wrong-method" {
						rule.Methods = new([]string{"Wrong-Method"})
					}
					policy.Rules = append(policy.Rules, rule)
				}
				fixture := newModelHTTPFixture(t, "shell", policy, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
				engine := fixture.env.srv.currentEngine()
				before := snapshotDNSLedger(engine)
				response, audit := fixture.request(t, route.method, route.host, route.path, nil, "")
				allowed := grant == "exact" || grant == "legacy"
				if allowed {
					if response.Code != 200 || fixture.requests.Load() != 1 {
						t.Fatal("the original matched endpoint permit did not reach its sole origin")
					}
					second, _ := fixture.request(t, route.method, route.host, route.path, nil, "")
					if second.Code != 403 || fixture.requests.Load() != 1 {
						t.Fatal("spent endpoint authority fell back to the broad host grant")
					}
				} else if response.Code != 403 || !strings.HasPrefix(audit.Rule, "service:") || fixture.lookups.Load() != 0 || fixture.dials.Load() != 0 || fixture.requests.Load() != 0 || !reflect.DeepEqual(before, snapshotDNSLedger(engine)) {
					t.Fatal("a broad host grant or wrong method bypassed the service barrier before ledger/origin")
				}
			})
		}
	}
}

func TestClassifierServiceInspectionAndNearPathsRetainOrdinaryPolicy(t *testing.T) {
	for _, route := range []struct{ host, method, path string }{
		{"discord.com", "GET", "/api/webhooksexample/id"},
		{"api.telegram.org", "POST", "/about"},
		{"pastebin.com", "GET", "/authored"},
		{"pastebin.com", "HEAD", "/authored"},
	} {
		kind, splice, err := httpClass(route.host)
		if err != nil || kind == "" || splice {
			t.Fatal("fixed path service was left opaque")
		}
		policy := api.Policy{Preset: api.PresetLocked, Rules: []api.Rule{{Layer: api.LayerSession, Target: route.host, Allow: true}}}
		fixture := newModelHTTPFixture(t, "shell", policy, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
		response, _ := fixture.request(t, route.method, route.host, route.path, nil, "")
		if response.Code != 200 || fixture.requests.Load() != 1 {
			t.Fatal("a path-only barrier became a host or read refusal")
		}
	}
}
