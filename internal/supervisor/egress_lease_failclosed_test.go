package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/link"
	"github.com/iamlakshya/simstim/internal/proto"
)

// This fixture deliberately has no asynchronous disarming worker. A lease
// failure must save a disarmed manifest itself, without relying on a later
// goroutine or tick winning the race with its snapshot.
type leaseFailureFixture struct {
	s            *Supervisor
	c            *egressCtl
	local, peer  *link.Session
	remote       *link.Stream
	manifestPath string
	auditPath    string
	applied      <-chan struct{}
}

func leaseFailureController(t *testing.T, rule api.Rule) *leaseFailureFixture {
	t.Helper()
	dir := testFolders(t)
	layers := []api.Layer{{Kind: api.LayerSession, Preset: api.PresetLocked, Rules: []api.Rule{rule}}}
	pol, cleaned, err := egress.MergeLayers(layers)
	if err != nil {
		t.Fatal(err)
	}
	s := &Supervisor{id: "failure2", m: &host.Manifest{Schema: 1, ID: "failure2", Name: "lease", Cwd: dir, CreatedAt: time.Now(), Armed: true, Layers: cleaned, Policy: &pol}, log: slog.New(slog.DiscardHandler)}
	f := &leaseFailureFixture{s: s, manifestPath: filepath.Join(dir, "manifest.json"), auditPath: filepath.Join(dir, "audit.jsonl")}
	if err := s.m.SaveTo(f.manifestPath); err != nil {
		t.Fatal(err)
	}
	s.audit = openAudit(f.auditPath, s.log)
	a, b := net.Pipe()
	f.local, f.peer = link.New(a, link.Supervisor, egress.LinkOptions()), link.New(b, link.Guest, egress.LinkOptions())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := f.local.Open(ctx, link.KindCtl, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.remote, err = f.peer.Accept(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ep := &egressProc{sess: f.local}
	f.c = &egressCtl{s: s, st: st, w: proto.NewWriter(st), waits: make(map[uint64]chan proto.Response), done: make(chan struct{}), events: newEgressEvents(), owner: ep}
	ep.ctl, s.eg = f.c, ep
	registerEventPolicy(t, f.c.events, rule)
	go func() { defer close(f.c.done); f.c.read() }()
	t.Cleanup(func() {
		_ = f.local.Close()
		_ = f.peer.Close()
		waitEventSignal(t, f.c.done, "lease failure reader shutdown")
		if f.applied != nil {
			waitEventSignal(t, f.applied, "lease failure operation shutdown")
		}
		s.audit.close()
	})
	return f
}

func failureRule() api.Rule {
	rule := api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "example.com", GrantID: "original", Once: true}
	rule.ID = api.RuleID(rule)
	return rule
}

func (f *leaseFailureFixture) apply(rule api.Rule) <-chan struct{} {
	done := make(chan struct{})
	f.applied = done
	go func() {
		defer close(done)
		f.s.polMu.Lock()
		defer f.s.polMu.Unlock()
		f.c.applyLeaseEnds(context.Background(), []leaseEnd{{identity: ruleLeaseIdentity(rule)}}, time.Now())
	}()
	return done
}

func (f *leaseFailureFixture) next(t *testing.T) proto.Request {
	t.Helper()
	if err := f.remote.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	frame, err := proto.Read(bufio.NewReader(f.remote))
	if err != nil {
		t.Fatal(err)
	}
	var req proto.Request
	if frame.Type != proto.TRequest || json.Unmarshal(frame.Payload, &req) != nil {
		t.Fatal("invalid lease removal request")
	}
	return req
}

func (f *leaseFailureFixture) blockControlWindow(t *testing.T) {
	t.Helper()
	// The remote application takes no more bytes. A timed-out write proves
	// that both its credit and our bounded send queue have filled.
	if err := f.c.st.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	n, err := f.c.st.Write(make([]byte, 3*link.DefaultWindow))
	if !errors.Is(err, os.ErrDeadlineExceeded) || n < link.DefaultWindow || n >= 3*link.DefaultWindow {
		t.Fatalf("control window was not blocked: wrote %d, error %v", n, err)
	}
	if err := f.c.st.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
}

func (f *leaseFailureFixture) saved(t *testing.T) host.Manifest {
	t.Helper()
	data, err := os.ReadFile(f.manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var m host.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLeaseNegativeReplyClosesTheOwnedLinkBeforeTakingStateLock(t *testing.T) {
	rule := failureRule()
	f := leaseFailureController(t, rule)
	applied := f.apply(rule)
	req := f.next(t)
	f.blockControlWindow(t)
	f.s.mu.Lock()
	// The asynchronous lifecycle closer would need this lock. The failure
	// path itself must close the owned link, even behind queued bytes.
	if err := proto.NewWriter(f.remote).WriteJSON(proto.TResponse, proto.Response{ID: req.ID, Error: &proto.Error{Code: "policy_error", Message: "rejected", Hint: "Check the policy."}}); err != nil {
		f.s.mu.Unlock()
		t.Fatal(err)
	}
	select {
	case <-f.local.Done():
	case <-time.After(time.Second):
		f.s.mu.Unlock()
		t.Fatal("lease rejection waited on a lingering stream instead of closing the owned link")
	}
	f.s.mu.Unlock()
	waitEventSignal(t, applied, "rejected lease persistence")
	saved := f.saved(t)
	if saved.Armed || len(saved.Layers[0].Rules) != 0 || saved.Policy == nil || len(saved.Policy.Rules) != 0 {
		t.Fatalf("rejected lease was not removed and disarmed on disk: %+v", saved)
	}
	data, err := os.ReadFile(f.auditPath)
	if err != nil || !strings.Contains(string(data), `"event":"rule_end"`) {
		t.Fatalf("rejected lease removal audit missing: %v\n%s", err, data)
	}
}

func TestLeaseMergeFailureClosesTheOwnedLinkAndPersistsDisarming(t *testing.T) {
	rule := failureRule()
	f := leaseFailureController(t, rule)
	// Preserve a valid previous policy, as a restored manifest might, while
	// making the remaining layer invalid under the current parser.
	f.s.m.Layers[0].Rules = append(f.s.m.Layers[0].Rules, api.Rule{Layer: api.LayerSession, Allow: true, Door: api.DoorNet, Target: "bad host"})
	f.blockControlWindow(t)
	applied := f.apply(rule)
	select {
	case <-f.local.Done():
	case <-time.After(time.Second):
		t.Fatal("lease merge failure left the owned link open behind its send queue")
	}
	waitEventSignal(t, applied, "failed merge persistence")
	saved := f.saved(t)
	if saved.Armed {
		t.Fatal("lease merge failure persisted an armed manifest")
	}
	if saved.Policy == nil || len(saved.Policy.Rules) != 1 || saved.Policy.Rules[0].GrantID != rule.GrantID {
		t.Fatal("failed merge replaced the last valid policy")
	}
	f.s.mu.Lock()
	armed, dirty := f.s.m.Armed, f.s.dirty
	f.s.mu.Unlock()
	if armed || !dirty {
		t.Fatal("lease merge failure was not marked disarmed for later saves")
	}
}

func TestLateLeaseReplyCannotChangeAReplacementOwner(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "accepted"}[accepted], func(t *testing.T) {
			rule := failureRule()
			f := leaseFailureController(t, rule)
			applied := f.apply(rule)
			req := f.next(t)
			a, b := net.Pipe()
			replacement := link.New(a, link.Supervisor, egress.LinkOptions())
			peer := link.New(b, link.Guest, egress.LinkOptions())
			t.Cleanup(func() { _ = replacement.Close(); _ = peer.Close() })
			fresh := rule
			fresh.GrantID = "replacement"
			f.s.mu.Lock()
			f.s.eg = &egressProc{sess: replacement}
			f.s.m.Layers[0].Rules[0] = fresh
			f.s.m.Policy.Rules[0] = fresh
			snap := f.s.snapshot()
			f.s.mu.Unlock()
			if err := f.s.save(&snap, false); err != nil {
				t.Fatal(err)
			}
			if err := proto.NewWriter(f.remote).WriteJSON(proto.TResponse, proto.Response{ID: req.ID, OK: accepted}); err != nil {
				t.Fatal(err)
			}
			waitEventSignal(t, applied, "late lease reply")
			if channelClosed(replacement.Done()) {
				t.Fatal("late lease failure closed a replacement link")
			}
			saved := f.saved(t)
			if !saved.Armed || len(saved.Layers[0].Rules) != 1 || saved.Layers[0].Rules[0].GrantID != fresh.GrantID || saved.Policy.Rules[0].GrantID != fresh.GrantID {
				t.Fatalf("late lease reply overwrote the replacement manifest: %+v", saved)
			}
			data, err := os.ReadFile(f.auditPath)
			if err != nil || strings.Contains(string(data), `"event":"rule_end"`) {
				t.Fatalf("late lease reply audited a replacement removal: %v\n%s", err, data)
			}
		})
	}
}
