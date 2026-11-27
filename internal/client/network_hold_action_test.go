package client

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func positiveHoldFixtureV1(t *testing.T, scope api.NetworkHoldScope, reason string) (*Conn, *GrantRequest, api.NetworkHoldActionArgs, *int) {
	t.Helper()
	h := h5GrantState(t)
	c := finishGeneratedTransportFixture(t, h, strings.Repeat("a", 32), api.FeatureNetworkHolds, api.FeatureNetworkPublicOnly, "session.id")
	c.pending = map[uint64]chan proto.Response{}
	a := api.NetworkHoldActionArgs{Version: api.NetworkHoldV1, Session: "hold2345", Request: strings.Repeat("c", 32), Scope: scope}
	selected := FileLayer{}
	if scope == api.NetworkHoldRepo {
		selected = FileLayer{Kind: api.LayerRepo, Name: "forge.example/team/app"}
	} else if scope == api.NetworkHoldProfile {
		selected = FileLayer{Kind: api.LayerProfile, Name: "review"}
	}
	r, err := NewNetworkHoldActionRequestV1(a, selected)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	answers := new(int)
	sharedGrantWire(t, c, func(req proto.Request) any {
		switch req.Op {
		case api.OpPolicyDescribe:
			return api.NetworkHoldDescriptor{Version: api.NetworkHoldV1, Request: a.Request, Revision: 7, Class: api.NetworkHoldPublicTarget, Expires: time.Now().Add(time.Minute), Reason: reason}
		case api.OpPolicyAnswer:
			*answers++
			var actual api.NetworkHoldActionArgs
			if json.Unmarshal(req.Args, &actual) != nil || actual.Request != a.Request || actual.Session != a.Session || actual.Scope != a.Scope || actual.Revision != 7 || actual.Maximum != api.NetworkHoldPublicOnly {
				t.Fatal("actual positive frame changed original identity or omitted its current restrictive maximum")
			}
			return api.NetworkHoldAnswerResult{Version: api.NetworkHoldV1, Scope: a.Scope, RuleIDs: []string{strings.Repeat("d", 32)}, MemoryOnly: true}
		default:
			t.Fatal("unexpected positive hold RPC", req.Op)
			return nil
		}
	})
	return c, r, a, answers
}

func TestPositiveHoldOnceUsesOriginalOpaqueIDAndActualDescriptorRevision(t *testing.T) {
	c, r, args, answers := positiveHoldFixtureV1(t, api.NetworkHoldOnce, api.NetworkHoldUploadBudget)
	ctx := WithGrantRequest(t.Context(), r)
	result, err := c.AnswerNetworkRequest(ctx, args)
	if err != nil || !result.MemoryOnly || *answers != 1 || len(result.RuleIDs) != 1 || r.networkHold.descriptor == nil || r.networkHold.descriptor.Reason != api.NetworkHoldUploadBudget {
		t.Fatal("once did not reach the exact original actor and purpose", err)
	}
	result.RuleIDs[0] = strings.Repeat("e", 32)
	again, err := c.AnswerNetworkRequest(ctx, args)
	if err != nil || *answers != 1 || again.RuleIDs[0] != strings.Repeat("d", 32) {
		t.Fatal("a retry resent the positive frame or shared mutable response storage", err)
	}
}

func TestPositiveHoldBudgetSessionRefusesBeforePositiveFrame(t *testing.T) {
	c, r, args, answers := positiveHoldFixtureV1(t, api.NetworkHoldSession, api.NetworkHoldUploadBudget)
	if _, err := c.AnswerNetworkRequest(WithGrantRequest(t.Context(), r), args); err == nil || *answers != 0 {
		t.Fatal("a non-sensitive budget hold acquired a session exception", err)
	}
}

func TestPositiveHoldCannotManufactureActionFromReadOnlyRequestOrRawDTO(t *testing.T) {
	c, r, args, answers := positiveHoldFixtureV1(t, api.NetworkHoldOnce, "")
	readOnly := NewNetworkHoldGrantRequest()
	defer readOnly.Close()
	for _, ctx := range []context.Context{t.Context(), WithGrantRequest(t.Context(), readOnly)} {
		if _, err := c.AnswerNetworkRequest(ctx, args); err == nil || *answers != 0 {
			t.Fatal("a read-only request or absent original action issued a positive frame", err)
		}
	}
	args.Revision, args.Maximum = 7, api.NetworkHoldExistingPolicy
	if _, err := c.AnswerNetworkRequest(WithGrantRequest(t.Context(), r), args); err == nil || *answers != 0 {
		t.Fatal("caller-supplied revision/maximum became publication authority", err)
	}
}

func TestPositiveHoldCannotRetargetConnectionOrRemovedSource(t *testing.T) {
	c, r, args, answers := positiveHoldFixtureV1(t, api.NetworkHoldOnce, "")
	ctx := WithGrantRequest(t.Context(), r)
	o, err := newNetworkHoldOperation(ctx, c, args)
	if err != nil {
		t.Fatal(err)
	}
	other := finishGeneratedTransportFixture(t, c.Host, c.HostID(), c.Features...)
	if _, err := other.AnswerNetworkRequest(ctx, args); err == nil || *answers != 0 {
		t.Fatal("a replacement accepted connection adopted the original action", err)
	}
	if err := SaveHosts(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := SaveHosts(t.Context(), []Host{c.Host}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AnswerNetworkRequest(ctx, args); err == nil || r.networkHold != o || *answers != 0 {
		t.Fatal("an old original action adopted remove/readd membership", err)
	}
}

func TestPositiveHoldRejectsFutureDescriptorAndMalformedCompleteAnswer(t *testing.T) {
	for _, corrupt := range []string{"descriptor-extension", "descriptor-reason", "answer-duplicate", "answer-future"} {
		t.Run(corrupt, func(t *testing.T) {
			c, r, args, _ := positiveHoldFixtureV1(t, api.NetworkHoldOnce, "")
			sharedGrantWire(t, c, func(req proto.Request) any {
				if req.Op == api.OpPolicyDescribe {
					result := map[string]any{"v": 1, "request": args.Request, "revision": 3, "class": "public", "expires": time.Now().Add(time.Minute)}
					if corrupt == "descriptor-extension" {
						result["future_permission"] = true
					} else if corrupt == "descriptor-reason" {
						result["reason"] = "future-budget"
					}
					return result
				}
				ids := []string{strings.Repeat("d", 32), strings.Repeat("d", 32)}
				version := 1
				if corrupt == "answer-future" {
					ids, version = ids[:1], 2
				}
				return api.NetworkHoldAnswerResult{Version: version, Scope: api.NetworkHoldOnce, RuleIDs: ids, MemoryOnly: true}
			})
			if _, err := c.AnswerNetworkRequest(WithGrantRequest(t.Context(), r), args); err == nil || r.networkHold.answerResult != nil {
				t.Fatal("unsupported or partial response became a completed answer", err)
			}
		})
	}
}

func TestPositiveHoldCurrentTierCanOnlyNarrowItsOriginalMaximum(t *testing.T) {
	c, r, args, _ := positiveHoldFixtureV1(t, api.NetworkHoldOnce, "")
	creation := h5Policy()
	creation.SessionID, creation.Ceiling, creation.CreationWall = args.Session, TierOwn, "process"
	creation.Secrets = nil
	if err := reserveSandbox(t.Context(), c.Host, creation); err != nil {
		t.Fatal(err)
	}
	ctx := WithGrantRequest(t.Context(), r)
	o, err := newNetworkHoldOperation(ctx, c, args)
	if err != nil || o.positiveArgs().Maximum != api.NetworkHoldExistingPolicy {
		t.Fatal("actual original Own history did not supply its initial maximum", err)
	}
	if err := SetTierContext(t.Context(), c.Host.Name, TierShared, c.HostID(), ""); err != nil {
		t.Fatal(err)
	}
	if err := o.checkLocal(ctx); err != nil || o.positiveArgs().Maximum != api.NetworkHoldPublicOnly {
		t.Fatal("a lowered current host tier did not narrow the maximum", err)
	}
	if err := SetTierContext(t.Context(), c.Host.Name, TierOwn, c.HostID(), ""); err != nil {
		t.Fatal(err)
	}
	if err := o.checkLocal(ctx); err != nil || o.positiveArgs().Maximum != api.NetworkHoldPublicOnly {
		t.Fatal("a later tier increase renewed the original action's maximum", err)
	}
}

func TestPositiveHoldExpiredDescriptorCannotIssueAnswer(t *testing.T) {
	c, r, args, answers := positiveHoldFixtureV1(t, api.NetworkHoldOnce, "")
	ctx := WithGrantRequest(t.Context(), r)
	o, err := newNetworkHoldOperation(ctx, c, args)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := o.prepare(ctx); err != nil {
		o.finish()
		t.Fatal(err)
	}
	o.finish()
	o.mu.Lock()
	o.descriptor.Expires = time.Now().Add(-time.Second)
	o.mu.Unlock()
	if _, err := c.AnswerNetworkRequest(ctx, args); err == nil || *answers != 0 {
		t.Fatal("an expired original descriptor issued a positive answer", err)
	}
}

func TestPositiveHoldAcceptsActualOpaqueDurableSessionAcknowledgement(t *testing.T) {
	c, r, args, _ := positiveHoldFixtureV1(t, api.NetworkHoldSession, "")
	answers := 0
	sharedGrantWire(t, c, func(request proto.Request) any {
		if request.Op == api.OpPolicyDescribe {
			return api.NetworkHoldDescriptor{Version: 1, Request: args.Request, Revision: 3, Class: api.NetworkHoldPublicTarget, Expires: time.Now().Add(time.Minute)}
		}
		if request.Op != api.OpPolicyAnswer {
			t.Fatal("durable session acknowledgement used an unrelated operation")
		}
		answers++
		// This is the real supervisor's complete public SESSION shape. Its
		// protected policy writer publishes neither a target nor rule IDs here.
		return api.NetworkHoldAnswerResult{Version: 1, Scope: api.NetworkHoldSession}
	})
	ctx := WithGrantRequest(t.Context(), r)
	result, err := c.AnswerNetworkRequest(ctx, args)
	if err != nil || result.MemoryOnly || len(result.RuleIDs) != 0 || answers != 1 || r.networkHold.answerResult == nil {
		t.Fatal("the actual durable acknowledgement was rejected or disclosed metadata", err)
	}
	if _, err := c.AnswerNetworkRequest(ctx, args); err != nil || answers != 1 {
		t.Fatal("a complete durable acknowledgement was sent again", err)
	}
}

func TestPositiveHoldRejectsUnproducedGroupAndDurableAcknowledgements(t *testing.T) {
	for _, kind := range []string{"distinct-groups", "ordinary-rule-as-group", "durable-rule-IDs", "once-without-memory"} {
		t.Run(kind, func(t *testing.T) {
			scope := api.NetworkHoldOnce
			if kind == "durable-rule-IDs" {
				scope = api.NetworkHoldSession
			}
			c, r, args, _ := positiveHoldFixtureV1(t, scope, "")
			answers := 0
			sharedGrantWire(t, c, func(request proto.Request) any {
				if request.Op == api.OpPolicyDescribe {
					return api.NetworkHoldDescriptor{Version: 1, Request: args.Request, Revision: 3, Class: api.NetworkHoldPublicTarget, Expires: time.Now().Add(time.Minute)}
				}
				answers++
				result := api.NetworkHoldAnswerResult{Version: 1, Scope: scope, MemoryOnly: true}
				switch kind {
				case "distinct-groups":
					result.RuleIDs = []string{strings.Repeat("d", 32), strings.Repeat("e", 32)}
				case "ordinary-rule-as-group":
					result.RuleIDs = []string{"gabcdef"}
				case "durable-rule-IDs":
					result.MemoryOnly, result.RuleIDs = false, []string{"gabcdef"}
				case "once-without-memory":
					result.MemoryOnly = false
				}
				return result
			})
			ctx := WithGrantRequest(t.Context(), r)
			if _, err := c.AnswerNetworkRequest(ctx, args); err == nil || r.networkHold.answerResult != nil || answers != 1 {
				t.Fatal("an unproduced acknowledgement became a completed answer", kind, err)
			}
			if _, err := c.AnswerNetworkRequest(ctx, args); err == nil || answers != 1 {
				t.Fatal("an uncertain answer was sent again", kind, err)
			}
		})
	}
}
