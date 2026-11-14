package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

type policyLifecycleConn struct {
	Conn
	reads     atomic.Int32
	allows    atomic.Int32
	mutations atomic.Int32
	entered   chan context.Context
	release   chan struct{}
}

func (c *policyLifecycleConn) Policy(ctx context.Context, id string) (api.Policy, error) {
	if err := ctx.Err(); err != nil {
		return api.Policy{}, err
	}
	c.reads.Add(1)
	return api.Policy{Preset: api.PresetBalanced, Rules: []api.Rule{{ID: id, Layer: api.LayerSession}}}, nil
}

func (c *policyLifecycleConn) AllowPolicy(ctx context.Context, a api.PolicyAddArgs) (api.Rule, error) {
	c.allows.Add(1)
	if c.entered != nil {
		c.entered <- ctx
		select {
		case <-ctx.Done():
		case <-c.release:
		}
	}
	if err := ctx.Err(); err != nil {
		return api.Rule{}, err
	}
	c.mutations.Add(1)
	return api.Rule{ID: "gabcdef", Allow: true, Target: a.Target, Layer: api.LayerSession}, nil
}

func (c *policyLifecycleConn) RemovePolicy(ctx context.Context, _ api.PolicyRmArgs) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	c.mutations.Add(1)
	return 1, nil
}

func (c *policyLifecycleConn) PolicyLog(ctx context.Context, _ api.PolicyLogArgs) ([]client.PolicyLogEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []client.PolicyLogEntry{}, nil
}

func policyLifecycleFixture(t *testing.T) (*harness, *hostConn, *policySheet, *policyLifecycleConn) {
	t.Helper()
	hs := sessions(t, 100, 36)
	h, s := hs.m.selectedPair()
	if h == nil || s == nil {
		t.Fatal("the policy fixture has no selected session")
	}
	c := &policyLifecycleConn{Conn: unclean(h.conn)}
	h.conn = cleanConn{c}
	d := &policySheet{host: h.name(), id: s.ID, name: s.Name, level: "process", origin: h.host, gen: h.gen,
		target: newInput("api.example.com", "")}
	hs.m.modal = d
	return hs, h, d, c
}

func TestPolicyMutationCancelledBeforeCommandStarts(t *testing.T) {
	for _, leave := range []string{"escape", "replacement", "dispose", "handoff", "close"} {
		t.Run(leave, func(t *testing.T) {
			hs, h, d, c := policyLifecycleFixture(t)
			cmd := hs.m.allowPolicy(d, false)
			if cmd == nil || !d.loading {
				t.Fatal("the actual allowance was not queued")
			}
			ctx := d.ctx
			switch leave {
			case "escape":
				_, _ = hs.m.Update(keyMsg("esc"))
			case "replacement":
				// A real async tier continuation can install another modal.
				// Update must retire the policy before any queued command runs.
				_, _ = hs.m.Update(hostTierResolvedMsg{host: h.host, gen: h.gen, tier: client.TierOwn,
					next: func() tea.Cmd { hs.m.modal = &helpSheet{}; return nil }})
			case "dispose":
				_ = hs.m.disposeModal()
				hs.m.modal = nil
			case "handoff":
				hs.m.pauseBg()
				hs.m.openBg(hs.m.ctx)
				if retry := hs.m.resumePolicy(); retry != nil {
					t.Fatal("handoff automatically retried the mutation")
				}
				if d.loading || d.err == nil {
					t.Fatal("handoff did not leave an explicit interrupted result")
				}
			case "close":
				hs.m.Close()
			}
			if ctx.Err() == nil {
				t.Fatal("the retired request context remains live")
			}
			if leave != "handoff" && leave != "close" && hs.m.backgroundContext().Err() != nil {
				t.Fatal("retiring one sheet cancelled the whole program")
			}
			if msg := cmd(); msg != nil || c.allows.Load() != 0 || c.mutations.Load() != 0 {
				t.Fatalf("a retired command reached the transport: msg=%T calls=%d mutations=%d", msg, c.allows.Load(), c.mutations.Load())
			}
		})
	}
}

func TestPolicyReadResultsKeepOwnerAndRequestSequence(t *testing.T) {
	hs, _, d, c := policyLifecycleFixture(t)
	first := hs.m.readPolicy(d)().(policyMsg)
	second := hs.m.readPolicy(d)().(policyMsg)
	if first.seq == second.seq {
		t.Fatal("a second read reused the first request")
	}
	first.notice = "stale mutation"
	if cmd := hs.m.onPolicy(first); cmd != nil || !d.loading || len(d.policy.Rules) != 0 {
		t.Fatal("a stale result changed the current read or scheduled another RPC")
	}
	if cmd := hs.m.onPolicy(second); cmd != nil || d.loading || len(d.policy.Rules) != 1 {
		t.Fatal("the current owned result was not installed")
	}
	if cmd := hs.m.onPolicy(second); cmd != nil || c.reads.Load() != 2 {
		t.Fatal("a delivered result was accepted twice")
	}
	third := hs.m.readPolicy(d)().(policyMsg)
	third.notice = "foreign result"
	foreign := &policySheet{seq: d.seq}
	hs.m.modal = foreign
	if cmd := hs.m.onPolicy(third); cmd != nil || foreign.err != nil || !d.loading {
		t.Fatal("a foreign sheet reused an old owner's live request")
	}
}

func TestPolicyConnectionGenerationChangeDropsResponse(t *testing.T) {
	hs, h, d, _ := policyLifecycleFixture(t)
	msg := hs.m.readPolicy(d)().(policyMsg)
	h.gen++
	if cmd := hs.m.onPolicy(msg); cmd != nil || len(d.policy.Rules) != 0 || d.loading || d.err == nil {
		t.Fatal("a response from an old host connection changed the sheet")
	}
}

func TestPolicyCloseCancelsAndJoinsStartedMutation(t *testing.T) {
	hs, _, d, c := policyLifecycleFixture(t)
	c.entered, c.release = make(chan context.Context, 1), make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(c.release) }) })
	cmd := hs.m.allowPolicy(d, false)
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	ctx := lifecycleWait(t, c.entered)
	closed := make(chan struct{})
	go func() { hs.m.Close(); close(closed) }()
	lifecycleWait(t, ctx.Done())
	lifecycleWait(t, closed)
	msg := lifecycleWait(t, result).(policyMsg)
	if !d.closed || c.mutations.Load() != 0 {
		t.Fatal("Close did not join the cancelled mutation")
	}
	if cmd := hs.m.onPolicy(msg); cmd != nil || c.reads.Load() != 0 {
		t.Fatal("a closed modal published a late result or reloaded its policy")
	}
}

func TestPolicySheetPasteAndMutationStopWhenClosed(t *testing.T) {
	hs, _, d, c := policyLifecycleFixture(t)
	d.adding = true
	_, _ = hs.m.Update(tea.PasteMsg{Content: "-suffix"})
	if d.target.value() != "api.example.com-suffix" {
		t.Fatal("paste did not reach the actual policy input")
	}
	d.stop()
	before := d.target.value()
	d.paste("stale")
	if d.target.value() != before || hs.m.allowPolicy(d, false) != nil || c.allows.Load() != 0 {
		t.Fatal("the closed modal accepted a paste or queued a mutation")
	}
}
