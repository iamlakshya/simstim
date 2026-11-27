package egress

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress/classify"
)

type classifierGrantBinding struct {
	source  *memberState
	engine  *Engine
	request Request
	hold    *networkHold
	expires time.Time
}

func (classifierGrantBinding) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

type memoryGrantResult struct {
	ID       string
	Decision Decision
}

func (memoryGrantResult) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, api.Redacted) }

func (result memoryGrantResult) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID         string `json:"id"`
		MemoryOnly bool   `json:"memory_only"`
	}{result.ID, true})
}

func endedPrivateGrant() error {
	return memberError("network_request_ended", "the pending request or its current policy has ended", "Refresh the current session and pending request before answering.")
}

func (s *Server) installClassifierGrantLocked(ctx context.Context, binding classifierGrantBinding, result classify.Result) (Decision, error) {
	if ctx == nil || ctx.Err() != nil || s.currentMembers() != binding.source || s.currentEngine() != binding.engine || binding.engine.unknown != api.UnknownClassify || s.holds == nil {
		return Decision{}, endedPrivateGrant()
	}
	scope := "session"
	if value := binding.source.metadata.Policy.Classifier.MaxScope; value != nil {
		scope = *value
	}
	result, err := classify.BoundResult(result, scope)
	if err != nil || result.Action != "allow" {
		return Decision{}, endedPrivateGrant()
	}
	h := s.holds
	h.mu.Lock()
	entry := binding.hold
	valid := entry != nil && h.byID[entry.meta.ID] == entry && entry.reason == "" && entry.meta.Expires.Equal(binding.expires) && s.now().Before(entry.meta.Expires) && entry.request.Host == binding.request.Host && entry.request.Port == binding.request.Port
	h.mu.Unlock()
	decision := binding.engine.evaluate(binding.request, nil, evalPrecheck)
	if !valid || decision.Allow || !decision.unmatched {
		return Decision{}, endedPrivateGrant()
	}
	installed, err := s.installMemoryGrantLocked(ctx, entry, api.NetworkHoldScope(result.Scope), false, "classifier", binding.request)
	return installed.Decision, err
}

// The caller holds policyMu. A public positive route additionally validates
// original source/maximum/revision before reaching this private target seam.
func (s *Server) installMemoryGrantLocked(ctx context.Context, entry *networkHold, scope api.NetworkHoldScope, publicOnly bool, by string, request Request) (memoryGrantResult, error) {
	if scope == api.NetworkHoldRepo || scope == api.NetworkHoldProfile {
		return memoryGrantResult{}, memberError("network_sensitive_scope", "a wider allowance would store this private target", "Choose once or session for a target that contains a known credential.")
	}
	if ctx == nil || ctx.Err() != nil || s.holds == nil || (scope != api.NetworkHoldOnce && scope != api.NetworkHoldSession) || (by != "live" && by != "classifier") {
		return memoryGrantResult{}, endedPrivateGrant()
	}
	state, current := s.currentMembers(), s.currentEngine()
	if state == nil {
		return memoryGrantResult{}, endedPrivateGrant()
	}
	h := s.holds
	h.mu.Lock()
	valid := !h.closed && entry != nil && h.byID[entry.meta.ID] == entry && entry.reason == "" && s.now().Before(entry.meta.Expires)
	if !valid {
		h.mu.Unlock()
		return memoryGrantResult{}, endedPrivateGrant()
	}
	targetRequest, original, expiry := entry.request, entry.originalHost, entry.meta.Expires
	if targetRequest.uploadBudgetOnly && scope != api.NetworkHoldOnce {
		h.mu.Unlock()
		return memoryGrantResult{}, uploadBudgetScopeError()
	}
	_, sensitive := publicNetworkHost(s.scrubber, targetRequest.Host, original)
	count := 1
	if scope == api.NetworkHoldOnce {
		count = max(1, int(entry.meta.Waiting))
	}
	h.mu.Unlock()
	if scope == api.NetworkHoldSession && !sensitive {
		return memoryGrantResult{}, memberError("network_publication_required", "this non-sensitive session allowance needs the existing durable policy writer", "Use the normal session policy writer until the scoped publication path is available.")
	}
	if request.Host != targetRequest.Host || request.Port != targetRequest.Port || request.uploadBudgetOnly != targetRequest.uploadBudgetOnly {
		return memoryGrantResult{}, endedPrivateGrant()
	}
	if _, err := netip.ParseAddr(targetRequest.Host); err == nil {
		// Every numeric answer is explicitly public-only, including Own.
		// Existing exact/CIDR exceptions already decided before unknown.
		publicOnly = true
		if _, allowed := s.dialer.CheckAddr(netip.MustParseAddr(targetRequest.Host), targetRequest.Port, nil); !allowed {
			return memoryGrantResult{}, memberError("network_address", "a held answer cannot create a private-address exception", "Use an independently approved numeric policy rule before retrying this address.")
		}
	}
	target := net.JoinHostPort(targetRequest.Host, strconv.Itoa(targetRequest.Port))
	row, err := compileTarget(target, true)
	if err != nil {
		return memoryGrantResult{}, endedPrivateGrant()
	}
	row.door = api.DoorNet
	row.ref = api.LayerRef{Kind: api.LayerSession}
	row.publicOnly = publicOnly
	row.memoryOnly = true
	row.uploadBudget = targetRequest.uploadBudgetOnly
	row.displaySensitive = sensitive
	row.by = by
	row.id = ""
	row.grantID = ""
	row.once = scope == api.NetworkHoldOnce
	rows := make([]compiledRule, count)
	for index := range rows {
		rows[index] = row
	}
	next := copyLiveGrantEngine(current)
	appendMemoryRows(next, rows)
	if err := checkMemoryGrantSize(ctx, state, next); err != nil {
		return memoryGrantResult{}, err
	}
	// Prospective matching is read-only. The new once rows are temporary
	// permanent witnesses until the SAME real ledger is rebound at commit.
	dry := copyGrantEngine(next)
	for index := range dry.layers {
		for at := range dry.layers[index].rules {
			if dry.layers[index].rules[at].memoryOnly && dry.layers[index].rules[at].id == row.id {
				dry.layers[index].rules[at].once = false
			}
		}
	}
	dryMode := evalPrecheck
	if targetRequest.uploadBudgetOnly {
		dryMode = evalBudgetPrecheck
	}
	if !dry.evaluate(request, nil, dryMode).Allow && (request.uploadBudgetOnly || request.Path != "" || !dry.permitsIntercept(request.Host, request.Port)) {
		return memoryGrantResult{}, memberError("policy_denied", "the current policy still refuses this target", "Review its current locks, denies, methods and service paths before answering.")
	}
	if next.unknown == api.UnknownClassify {
		index, err := buildClassifierNameIndex(next)
		if err != nil {
			return memoryGrantResult{}, err
		}
		next.classifierNames = index
	}
	group, err := registryNonce()
	if err != nil {
		return memoryGrantResult{}, err
	}
	for _, layer := range current.layers {
		for _, existing := range layer.rules {
			if existing.id == group {
				return memoryGrantResult{}, endedPrivateGrant()
			}
		}
	}
	grants := make(map[string]struct{}, count)
	for index := range rows {
		id, err := registryNonce()
		if err != nil {
			return memoryGrantResult{}, err
		}
		if _, exists := grants[id]; exists {
			return memoryGrantResult{}, endedPrivateGrant()
		}
		grants[id] = struct{}{}
		rows[index].id = group
		rows[index].grantID = id
	}
	// Replace only the prospective new placeholder rows, not an older grant.
	for index := range next.layers {
		for at := range next.layers[index].rules {
			r := &next.layers[index].rules[at]
			if r.memoryOnly && r.id == row.id {
				*r = rows[0]
				rows = rows[1:]
			}
		}
	}
	s.ledger.mu.Lock()
	h.mu.Lock()
	if ctx.Err() != nil || s.currentMembers() != state || s.currentEngine() != current || h.closed || h.byID[entry.meta.ID] != entry || entry.reason != "" || !entry.meta.Expires.Equal(expiry) || !s.now().Before(expiry) || entry.request.Host != targetRequest.Host || entry.request.Port != targetRequest.Port {
		h.mu.Unlock()
		s.ledger.mu.Unlock()
		return memoryGrantResult{}, endedPrivateGrant()
	}
	// New attachments need policyMu, so only cancellation can have reduced
	// the prepared count. An answer linearises with that current leaf count.
	keep := 1
	if scope == api.NetworkHoldOnce {
		keep = max(1, int(entry.meta.Waiting))
	}
	var budgetOwners []*Permit
	if targetRequest.uploadBudgetOnly {
		for _, owner := range entry.waiters {
			if owner == nil || owner.ledger != s.ledger || owner.closed || !owner.committed {
				h.mu.Unlock()
				s.ledger.mu.Unlock()
				return memoryGrantResult{}, endedPrivateGrant()
			}
			budgetOwners = append(budgetOwners, owner)
		}
	}
	for index := range next.layers {
		kept := next.layers[index].rules[:0]
		for _, candidate := range next.layers[index].rules {
			if candidate.memoryOnly && candidate.id == group {
				if keep == 0 {
					continue
				}
				keep--
			}
			kept = append(kept, candidate)
		}
		next.layers[index].rules = kept
	}
	s.ledger.syncPolicyLocked(next)
	// The original paused connections already committed their network
	// permits. Bind their byte continuations in this SAME ledger before
	// publishing or waking them; another flow cannot steal those slots.
	for _, layer := range next.layers {
		for _, candidate := range layer.rules {
			if candidate.uploadBudget && candidate.id == group && len(budgetOwners) > 0 {
				owner := budgetOwners[0]
				budgetOwners = budgetOwners[1:]
				candidate.slot.owner, candidate.slot.state = owner, slotCommitted
				owner.owned[candidate.slot] = struct{}{}
			}
		}
	}
	s.mu.Lock()
	s.engine = next
	s.mu.Unlock()
	h.finishLocked(entry, "answered", s.now())
	h.mu.Unlock()
	s.ledger.mu.Unlock()
	s.wakeExpiry()
	return memoryGrantResult{ID: group, Decision: next.evaluate(request, nil, evalPrecheck)}, nil
}
