package supervisor

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/paths"
)

func TestSupervisedOnceFlowEndsItsExactGrantAndCanBeRegranted(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer ts.Close()
	const id = "onceend2"
	_, proxyURL := startEgressSession(t, id, "shell", api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownDeny}, "", ts.Listener.Addr().String(), "")
	hc := proxyClient(t, proxyURL)
	transport := hc.Transport.(*http.Transport)
	transport.DisableKeepAlives = true
	t.Cleanup(transport.CloseIdleConnections)
	add := func() api.Rule {
		t.Helper()
		raw, err := host.HolderRequest(id, "policy.add", api.PolicyAddArgs{Allow: true, Target: "example.com", Once: true}, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		var rule api.Rule
		if err := json.Unmarshal(raw, &rule); err != nil || rule.GrantID == "" {
			t.Fatalf("new grant = %+v, %v", rule, err)
		}
		return rule
	}
	request := func() {
		t.Helper()
		resp, err := hc.Get("https://example.com/once")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Fatalf("once request = status %d body %q error %v", resp.StatusCode, body, err)
		}
	}
	first := add()
	request()
	waitFor(t, "first once lease removed and audited", func() bool { return trustedLeaseGone(id, first) && leaseEndInAudit(id, first) })
	assertLeaseAuditOrder(t, id, first, 1)
	second := add()
	if second.ID != first.ID || second.GrantID == first.GrantID {
		t.Fatalf("explicit regrant changed the public ID or reused its instance: %+v to %+v", first, second)
	}
	request()
	waitFor(t, "second once lease removed and audited", func() bool { return trustedLeaseGone(id, second) && leaseEndInAudit(id, second) })
	assertLeaseAuditOrder(t, id, second, 2)
	if resp, err := hc.Get("https://example.com/spent"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Fatalf("spent grant still authorised a connection: %v", err)
	}
}

func TestSupervisedIdleTimedGrantEndsWithoutAFlow(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer ts.Close()
	const id = "idleend2"
	_, _ = startEgressSession(t, id, "shell", api.Policy{Preset: api.PresetLocked, Unknown: api.UnknownDeny}, "", ts.Listener.Addr().String(), "")
	raw, err := host.HolderRequest(id, "policy.add", api.PolicyAddArgs{Allow: true, Target: "example.com", Until: time.Now().Add(300 * time.Millisecond)}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var rule api.Rule
	if err := json.Unmarshal(raw, &rule); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "idle timed lease removed and audited", func() bool { return trustedLeaseGone(id, rule) && leaseEndInAudit(id, rule) })
	data, err := os.ReadFile(paths.AuditLog(id))
	if err != nil || !strings.Contains(string(data), `"event":"rule_end"`) || !strings.Contains(string(data), `"reason":"expired"`) || !strings.Contains(string(data), rule.GrantID) {
		t.Fatalf("missing idle expiry audit: %v\n%s", err, data)
	}
}

func trustedLeaseGone(id string, rule api.Rule) bool {
	m, err := host.LoadManifest(id)
	if err != nil || m.Policy == nil {
		return false
	}
	for _, r := range m.Policy.Rules {
		if r.ID == rule.ID && r.GrantID == rule.GrantID && r.Until.Equal(rule.Until) {
			return false
		}
	}
	for _, l := range m.Layers {
		for _, r := range l.Rules {
			if r.ID == rule.ID && r.GrantID == rule.GrantID && r.Until.Equal(rule.Until) {
				return false
			}
		}
	}
	return true
}

func leaseEndInAudit(id string, rule api.Rule) bool {
	data, err := os.ReadFile(paths.AuditLog(id))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		var entry struct {
			Event  string `json:"event"`
			Fields struct {
				ID      string `json:"id"`
				GrantID string `json:"grant_id"`
			} `json:"fields"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Event == "rule_end" && entry.Fields.ID == rule.ID && entry.Fields.GrantID == rule.GrantID {
			return true
		}
	}
	return false
}

func assertLeaseAuditOrder(t *testing.T, id string, rule api.Rule, expectedNets int) {
	t.Helper()
	data, err := os.ReadFile(paths.AuditLog(id))
	if err != nil {
		t.Fatal(err)
	}
	netIndex, endIndex, netCount := -1, -1, 0
	for i, line := range strings.Split(string(data), "\n") {
		var entry map[string]any
		if json.Unmarshal([]byte(line), &entry) != nil {
			continue
		}
		if entry["kind"] == "net" && entry["host"] == "example.com" && entry["decision"] == "allow" {
			netIndex = i
			netCount++
		}
		if entry["event"] == "rule_end" {
			fields, ok := entry["fields"].(map[string]any)
			if ok && fields["id"] == rule.ID && fields["grant_id"] == rule.GrantID {
				endIndex = i
				break
			}
		}
	}
	if netIndex < 0 || endIndex <= netIndex || netCount != expectedNets {
		t.Fatalf("a completed flow's audit did not precede its exact rule_end: net=%d end=%d\n%s", netIndex, endIndex, data)
	}
}
