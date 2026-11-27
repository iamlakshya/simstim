package egress

import (
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHTTP2ActiveOnceRequestSurvivesUnrelatedReloadWithoutRearming(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "api.example.com/held", GrantID: "held-instance", Once: true})
	f := newHTTPLifetimeFixture(t, pol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "first")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = io.WriteString(w, "second")
		case <-r.Context().Done():
		}
	}), nil)
	response, err := f.client.Get("https://api.example.com/held")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	requireHTTPPrefix(t, response, "first")
	f.env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: pol})
	other, err := f.client.Get("https://api.example.com/held")
	if err != nil {
		t.Fatalf("existing guest TLS was closed by the reserved once grant: %v", err)
	}
	_, _ = io.Copy(io.Discard, other.Body)
	_ = other.Body.Close()
	if other.StatusCode != http.StatusForbidden || f.dials.Load() != 1 || f.guestDials.Load() != 1 {
		t.Fatalf("reload rearmed or replaced the first request: status=%d origins=%d guests=%d", other.StatusCode, f.dials.Load(), f.guestDials.Load())
	}
	releaseOnce.Do(func() { close(release) })
	if rest, err := io.ReadAll(response.Body); err != nil || string(rest) != "second" {
		t.Fatalf("the first permitted request lost its final bytes: %q, %v", rest, err)
	}
	f.env.waitAudits(t, 2)
	waitHTTPState(t, func() bool { f.env.mu.Lock(); defer f.env.mu.Unlock(); return len(f.env.ended) == 1 }, "the exact request's once end")
	f.env.mu.Lock()
	defer f.env.mu.Unlock()
	if len(f.env.ended) != 1 || f.env.ended[0].ID != pol.Rules[0].ID || f.env.ended[0].GrantID != "held-instance" {
		t.Fatalf("wrong or duplicate once end: %+v", f.env.ended)
	}
	auditAt, endAt := -1, -1
	for i, event := range f.env.events {
		if event == EventAudit {
			auditAt = i
		}
		if event == EventOnceEnd {
			endAt = i
		}
	}
	if auditAt < 0 || endAt <= auditAt {
		t.Fatalf("once end preceded the completed request audit: %v", f.env.events)
	}
}

func TestConcurrentHTTP2RequestsUseDistinctCheckedOriginsAndRevocationKeepsSibling(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	revokedOrigin := make(chan struct{})
	pol := onceTestPolicy(t, api.PresetLocked,
		api.Rule{Allow: true, Target: "api.example.com/first", GrantID: "first-instance", Once: true},
		api.Rule{Allow: true, Target: "api.example.com/second", GrantID: "second-instance", Once: true})
	f := newHTTPLifetimeFixture(t, pol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "open")
		w.(http.Flusher).Flush()
		if r.URL.Path == "/first" {
			<-r.Context().Done()
			close(revokedOrigin)
			return
		}
		select {
		case <-release:
			_, _ = io.WriteString(w, "survived")
		case <-r.Context().Done():
		}
	}), nil)
	first, err := f.client.Get("https://api.example.com/first")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Body.Close()
	requireHTTPPrefix(t, first, "open")
	second, err := f.client.Get("https://api.example.com/second")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Body.Close()
	requireHTTPPrefix(t, second, "open")
	// Both origin responses are still active. A sequential request test
	// cannot catch a shared H2 connection that bypasses the second dial.
	if f.dials.Load() != 2 || f.guestDials.Load() != 1 {
		t.Fatalf("concurrent requests shared an origin dial or replaced guest TLS: origins=%d guests=%d", f.dials.Load(), f.guestDials.Load())
	}
	kept := pol
	kept.Rules = nil
	for _, rule := range pol.Rules {
		if rule.Target == "api.example.com/second" {
			kept.Rules = append(kept.Rules, rule)
		}
	}
	f.env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: kept})
	waitHTTPProgress(t, revokedOrigin, "only the revoked request's origin to close")
	if data, err := io.ReadAll(first.Body); err == nil {
		t.Fatalf("revoked streaming response completed normally with %q", data)
	}
	releaseOnce.Do(func() { close(release) })
	if rest, err := io.ReadAll(second.Body); err != nil || string(rest) != "survived" {
		t.Fatalf("revoking one H2 request closed its permitted sibling: %q, %v", rest, err)
	}
	if f.dials.Load() != 2 || f.guestDials.Load() != 1 {
		t.Fatal("the surviving sibling was silently retried on a different connection")
	}
	waitHTTPState(t, func() bool {
		return len(f.env.srv.flows.snapshot()) == 0 && len(f.env.srv.httpSem) == 0 && len(f.env.srv.originSem) == 0
	}, "both independent request lifetimes to finish")
	// A control reply on the same byte stream follows all completed audit
	// and end writes. It is an ordering barrier, not a timing assumption.
	f.env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: kept})
	f.env.mu.Lock()
	ended := append([]OnceEndEvent(nil), f.env.ended...)
	f.env.mu.Unlock()
	seen := make(map[string]bool)
	for _, end := range ended {
		if seen[end.GrantID] {
			t.Fatalf("duplicate request end event: %+v", ended)
		}
		seen[end.GrantID] = true
	}
	if len(ended) != 2 || !seen["first-instance"] || !seen["second-instance"] {
		t.Fatalf("request ownership leaked or crossed grant identities: %+v", ended)
	}
}

func TestHTTP2DeadlineCancelsTheExactActiveRequest(t *testing.T) {
	at := time.Now()
	var clockMu sync.Mutex
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return at }
	stopped := make(chan struct{})
	pol := onceTestPolicy(t, api.PresetLocked, api.Rule{Allow: true, Target: "api.example.com/timed", Until: at.Add(time.Hour)})
	f := newHTTPLifetimeFixture(t, pol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "open")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(stopped)
	}), clock)
	response, err := f.client.Get("https://api.example.com/timed")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	requireHTTPPrefix(t, response, "open")
	clockMu.Lock()
	at = at.Add(2 * time.Hour)
	clockMu.Unlock()
	f.env.srv.expire(f.env.srv.currentEngine(), clock())
	waitHTTPProgress(t, stopped, "expired HTTP request origin")
	if _, err := io.ReadAll(response.Body); err == nil {
		t.Fatal("expired response completed normally")
	}
}
