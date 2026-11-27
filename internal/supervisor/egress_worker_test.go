package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

type eventFixture struct {
	s          *Supervisor
	c          *egressCtl
	peer       *link.Session
	local      *link.Session
	writer     *proto.Writer
	requests   chan proto.Request
	readerDone chan struct{}
	auditPath  string
}

func waitEventSignal(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for " + name)
	}
}

func eventController(t *testing.T, rules ...api.Rule) *eventFixture {
	t.Helper()
	dir := testFolders(t)
	layers := []api.Layer{{Kind: api.LayerSession, Preset: api.PresetLocked, Rules: rules}}
	pol, cleaned, err := egress.MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}
	s := &Supervisor{id: "lease222", firstSaved: true, m: &host.Manifest{Schema: 1, ID: "lease222", Name: "lease", Cwd: dir, CreatedAt: time.Now(), Layers: cleaned, Policy: &pol}, log: slog.New(slog.DiscardHandler)}
	auditPath := filepath.Join(dir, "events.jsonl")
	s.audit = openAudit(auditPath, s.log)
	a, b := net.Pipe()
	local := link.New(a, link.Supervisor, egress.LinkOptions())
	peer := link.New(b, link.Guest, egress.LinkOptions())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := local.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := peer.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ep := &egressProc{sess: local}
	s.eg = ep
	c := newEgressCtl(ctx, s, st, ep)
	ep.ctl = c
	f := &eventFixture{s: s, c: c, local: local, peer: peer, writer: proto.NewWriter(remote), requests: make(chan proto.Request, 16), readerDone: make(chan struct{}), auditPath: auditPath}
	go func() {
		defer close(f.readerDone)
		r := bufio.NewReader(remote)
		for {
			frame, err := proto.Read(r)
			if err != nil {
				return
			}
			var req proto.Request
			if frame.Type == proto.TRequest && json.Unmarshal(frame.Payload, &req) == nil {
				select {
				case f.requests <- req:
				case <-local.Done():
					return
				}
			}
		}
	}()
	registerEventPolicy(t, c.events, rules...)
	t.Cleanup(func() {
		_ = local.Close()
		_ = peer.Close()
		waitEventSignal(t, c.events.finished, "event worker shutdown")
		waitEventSignal(t, c.events.controlClosed, "control shutdown")
		s.wg.Wait()
		s.audit.close()
	})
	return f
}

func (f *eventFixture) event(t *testing.T, event egress.CtlEvent) {
	t.Helper()
	if err := f.writer.WriteJSON(proto.TEvent, event); err != nil {
		t.Fatal(err)
	}
}

func (f *eventFixture) reply(t *testing.T, req proto.Request) {
	t.Helper()
	if err := f.writer.WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: true}); err != nil {
		t.Fatal(err)
	}
}

func (f *eventFixture) next(t *testing.T) proto.Request {
	t.Helper()
	select {
	case req := <-f.requests:
		return req
	case <-time.After(10 * time.Second):
		t.Fatal("no control request")
		return proto.Request{}
	}
}

func TestAuditIOCannotBlockTheSoleReplyReader(t *testing.T) {
	f := eventController(t)
	f.s.audit.mu.Lock()
	var release sync.Once
	unlock := func() { release.Do(f.s.audit.mu.Unlock) }
	t.Cleanup(unlock)
	f.event(t, egress.CtlEvent{Event: egress.EventAudit, Audit: &egress.AuditEntry{Host: "example.com"}})
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- f.c.request(ctx, "probe", nil, nil)
	}()
	f.reply(t, f.next(t))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("audit I/O blocked an unrelated RPC reply")
	}
	unlock()
}

func TestEndBeforePolicyAddReplyDoesNotDeadlockOrRearm(t *testing.T) {
	f := eventController(t)
	completed := make(chan error, 1)
	go func() {
		_, err := f.s.applyLivePolicyAdd(api.PolicyAddArgs{Allow: true, Target: "example.com", Once: true})
		if err != nil {
			completed <- err
		} else {
			completed <- nil
		}
	}()
	add := f.next(t)
	var update egress.PolicyUpdateArgs
	if err := json.Unmarshal(add.Args, &update); err != nil || len(update.Policy.Rules) != 1 {
		t.Fatalf("bad policy add: %+v, %v", update, err)
	}
	rule := update.Policy.Rules[0]
	f.event(t, egress.CtlEvent{Event: egress.EventAudit, Audit: &egress.AuditEntry{Host: "example.com", Rule: rule.ID}})
	f.event(t, onceEvent(rule))
	f.reply(t, add)
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("end event blocked publication of its policy add")
	}
	remove := f.next(t)
	var removed egress.PolicyUpdateArgs
	if err := json.Unmarshal(remove.Args, &removed); err != nil || len(removed.Policy.Rules) != 0 {
		t.Fatalf("spent lease survived the update: %+v, %v", removed, err)
	}
	f.reply(t, remove)
	waitFor(t, "spent lease removal", func() bool { f.s.mu.Lock(); defer f.s.mu.Unlock(); return len(f.s.m.Layers[0].Rules) == 0 })
	waitFor(t, "rule end audit", func() bool { b, _ := os.ReadFile(f.auditPath); return strings.Contains(string(b), "rule_end") })
	b, err := os.ReadFile(f.auditPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	netAt, addAt, endAt := strings.Index(text, `"kind":"net"`), strings.Index(text, `"op":"add"`), strings.Index(text, `"event":"rule_end"`)
	if netAt < 0 || addAt < 0 || endAt < netAt || endAt < addAt {
		t.Fatalf("audit ordering changed: %s", text)
	}
	registerEventPolicy(t, f.c.events, rule)
	// A delayed duplicate cannot put the spent grant back into the manifest.
	f.event(t, onceEvent(rule))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	probe := make(chan error, 1)
	go func() { probe <- f.c.request(ctx, "probe", nil, nil) }()
	f.reply(t, f.next(t))
	if err := <-probe; err != nil {
		t.Fatal(err)
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	if len(f.s.m.Layers[0].Rules) != 0 {
		t.Fatal("duplicate event rearmed a spent grant")
	}
}

func TestIdleExpiryRemovesOnlyItsExactLease(t *testing.T) {
	rule := api.Rule{ID: "rule", GrantID: "identity", Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "example.com", Until: time.Now().Add(-time.Second)}
	rule.ID = api.RuleID(rule)
	f := eventController(t, rule)
	f.event(t, expiryEvent(rule))
	f.reply(t, f.next(t))
	waitFor(t, "idle expiry removal", func() bool { f.s.mu.Lock(); defer f.s.mu.Unlock(); return len(f.s.m.Layers[0].Rules) == 0 })
}
