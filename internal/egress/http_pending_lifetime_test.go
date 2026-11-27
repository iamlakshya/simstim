package egress

import (
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
)

func TestHTTP2PendingGzipCancellationLeavesItsStreamingSiblingAlive(t *testing.T) {
	for _, cause := range []string{"revoke", "deadline"} {
		t.Run(cause, func(t *testing.T) {
			at := time.Now()
			var clockMu sync.Mutex
			clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return at }
			upload := api.Rule{Allow: true, Target: "api.example.com/upload", Once: true, GrantID: "pending-instance"}
			if cause == "deadline" {
				upload.Until = at.Add(time.Hour)
			}
			pol := onceTestPolicy(t, api.PresetLocked, upload, api.Rule{Allow: true, Target: "api.example.com/held"})
			release := make(chan struct{})
			var releaseOnce sync.Once
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			f := newHTTPLifetimeFixture(t, pol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "open")
				w.(http.Flusher).Flush()
				select {
				case <-release:
					_, _ = io.WriteString(w, "survived")
				case <-r.Context().Done():
				}
			}), clock)
			held, err := f.client.Get("https://api.example.com/held")
			if err != nil {
				t.Fatal(err)
			}
			defer held.Body.Close()
			requireHTTPPrefix(t, held, "open")
			body, producer := io.Pipe()
			defer body.Close()
			defer producer.Close()
			req, err := http.NewRequest(http.MethodPost, "https://api.example.com/upload", body)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Encoding", "gzip")
			type outcome struct {
				response *http.Response
				err      error
			}
			finished := make(chan outcome, 1)
			go func() { response, err := f.client.Do(req); finished <- outcome{response, err} }()
			// A valid gzip header needs more bytes. No EOF is supplied, so
			// the request stays pending before any checked origin dial.
			if _, err := producer.Write([]byte{0x1f}); err != nil {
				t.Fatal(err)
			}
			waitHTTPState(t, func() bool {
				return !f.env.srv.currentEngine().evaluateHTTP(Request{Host: "api.example.com", Port: 443, Path: "/upload", Method: http.MethodPost}, nil, evalPrecheck).Allow
			}, "the pending upload's exclusive reservation")
			if f.dials.Load() != 1 {
				t.Fatal("an incomplete gzip header dialled an origin")
			}
			if cause == "revoke" {
				kept := pol
				kept.Rules = nil
				for _, rule := range pol.Rules {
					if rule.Target == "api.example.com/held" {
						kept.Rules = append(kept.Rules, rule)
					}
				}
				f.env.call(t, CtlPolicyUpdate, PolicyUpdateArgs{Policy: kept})
			} else {
				clockMu.Lock()
				at = at.Add(2 * time.Hour)
				clockMu.Unlock()
				f.env.srv.expire(f.env.srv.currentEngine(), clock())
			}
			select {
			case got := <-finished:
				if got.response != nil {
					_, _ = io.Copy(io.Discard, got.response.Body)
					_ = got.response.Body.Close()
					if got.response.StatusCode == http.StatusOK {
						t.Fatal("a cancelled pending upload reported success")
					}
				} else if got.err == nil {
					t.Fatal("cancelled request returned neither a response nor an error")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("policy cancellation left gzip header decoding blocked")
			}
			releaseOnce.Do(func() { close(release) })
			if rest, err := io.ReadAll(held.Body); err != nil || string(rest) != "survived" {
				t.Fatalf("pending upload cancellation tore down its sibling: %q, %v", rest, err)
			}
			waitHTTPState(t, func() bool {
				return len(f.env.srv.flows.snapshot()) == 0 && len(f.env.srv.httpSem) == 0 && len(f.env.srv.originSem) == 0
			}, "all request reservations, scopes and origin slots to finish")
			f.env.mu.Lock()
			ended := append([]OnceEndEvent(nil), f.env.ended...)
			f.env.mu.Unlock()
			if len(ended) != 0 || f.dials.Load() != 1 || f.guestDials.Load() != 1 {
				t.Fatalf("pending cancellation spent a grant or restarted a sibling: ends=%v origins=%d guests=%d", ended, f.dials.Load(), f.guestDials.Load())
			}
		})
	}
}
