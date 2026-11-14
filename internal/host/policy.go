package host

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/paths"
	"github.com/iamlakshya/simstim/internal/proto"
)

// ApplyPolicyAdd adds or updates a rule in the session layer and merges layers.
func ApplyPolicyAdd(layers []api.Layer, a api.PolicyAddArgs) (api.Policy, []api.Layer, api.Rule, error) {
	door := a.Door
	if door == "" {
		door = api.DoorNet
	}
	if door != api.DoorNet {
		return api.Policy{}, nil, api.Rule{}, UserError("bad_door", fmt.Sprintf("unsupported door %q", door), "Pass net, or omit --door.")
	}
	norm, err := egress.ValidateRuleTarget(a.Target)
	if err != nil {
		return api.Policy{}, nil, api.Rule{}, err
	}
	methods, err := api.CanonicalHTTPMethods(a.Methods)
	if err != nil {
		return api.Policy{}, nil, api.Rule{}, UserError("bad_method", err.Error(), "Pass each method as one exact HTTP token, such as GET or POST.")
	}
	candidate, err := egress.CanonicalRuleSelection(api.Rule{Layer: api.LayerSession, Allow: a.Allow, Door: door, Target: norm, Mode: a.Mode, Methods: methods, Tools: a.Tools})
	if err != nil {
		return api.Policy{}, nil, api.Rule{}, err
	}
	if _, _, err := egress.MergeLayers(layers); err != nil {
		return api.Policy{}, nil, api.Rule{}, err
	}
	var instance [16]byte
	if _, err := rand.Read(instance[:]); err != nil {
		return api.Policy{}, nil, api.Rule{}, UserError("policy_grant", "cannot create a grant instance", "Try the policy command again.")
	}
	r := api.Rule{
		GrantID: hex.EncodeToString(instance[:]),
		Layer:   api.LayerSession,
		Allow:   a.Allow,
		Door:    door,
		Target:  norm,
		Mode:    a.Mode,
		Methods: methods,
		Tools:   candidate.Tools,
		Once:    a.Once,
		Until:   a.Until,
	}
	r.ID = api.RuleID(r)

	next := cloneLayers(layers)
	idx := slices.IndexFunc(next, func(l api.Layer) bool { return l.Kind == api.LayerSession })
	if idx < 0 {
		next = append(next, api.Layer{Kind: api.LayerSession, Rules: []api.Rule{r}})
	} else {
		kept := next[idx].Rules[:0]
		for _, old := range next[idx].Rules {
			match, err := egress.MatchesRuleReplacement(old, r)
			if err != nil {
				return api.Policy{}, nil, api.Rule{}, err
			}
			if match {
				continue
			}
			kept = append(kept, old)
		}
		kept = append(kept, r)
		next[idx].Rules = kept
	}
	pol, cleaned, err := egress.MergeLayers(next)
	if err != nil {
		return api.Policy{}, nil, api.Rule{}, err
	}
	return pol, cleaned, r, nil
}

// ApplyPolicyRm removes matching rules from the session layer and merges layers.
func ApplyPolicyRm(layers []api.Layer, a api.PolicyRmArgs) (api.Policy, []api.Layer, int, error) {
	wantID := strings.TrimSpace(a.ID)
	wantTarget := strings.TrimSpace(a.Target)
	if wantID == "" && wantTarget == "" {
		return api.Policy{}, nil, 0, UserError("bad_request", "policy rm requires a rule ID or target",
			"List rules with policy ls, then pass a rule ID or target.")
	}
	if wantTarget != "" {
		if norm, err := egress.ValidateRuleTarget(wantTarget); err == nil {
			wantTarget = norm
		}
	}
	normalised := cloneLayers(layers)
	for i := range normalised {
		if normalised[i].Kind != api.LayerSession {
			continue
		}
		for j := range normalised[i].Rules {
			normalised[i].Rules[j].Layer, normalised[i].Rules[j].Name, normalised[i].Rules[j].Source = normalised[i].Kind, normalised[i].Name, normalised[i].Source
		}
		// Canonicalise without admitting a colliding set: explicit target
		// removal may repair it; ID selection still refuses ambiguity.
		for j, old := range normalised[i].Rules {
			canonical, err := egress.CanonicalRuleSelection(old)
			if err != nil {
				return api.Policy{}, nil, 0, err
			}
			normalised[i].Rules[j] = canonical
		}
	}
	layers = normalised
	if wantID != "" {
		for _, layer := range layers {
			if layer.Kind != api.LayerSession {
				continue
			}
			candidates := slices.Clone(layer.Rules)
			for j := range candidates {
				candidates[j].Layer, candidates[j].Name, candidates[j].Source = layer.Kind, layer.Name, layer.Source
			}
			if _, err := egress.CheckRuleIDSelection(candidates, wantID, api.LayerRef{Kind: layer.Kind, Name: layer.Name, Source: layer.Source}); err != nil {
				return api.Policy{}, nil, 0, err
			}
		}
	}
	if wantID != "" {
		wantTarget = ""
	}
	next := cloneLayers(layers)
	removed := 0
	for i := range next {
		if next[i].Kind != api.LayerSession {
			continue
		}
		var kept []api.Rule
		for _, r := range next[i].Rules {
			matchID := wantID != "" && r.ID == wantID
			matchTarget := wantTarget != "" && r.Target == wantTarget && (a.Door == "" || r.Door == a.Door)
			if matchID || matchTarget {
				removed++
				continue
			}
			kept = append(kept, r)
		}
		next[i].Rules = kept
	}
	if removed == 0 {
		return api.Policy{}, nil, 0, UserError("not_found", "no matching rule in the session layer",
			"Only session rules can be removed live. Run policy ls to see rule IDs and layers.")
	}
	pol, cleaned, err := egress.MergeLayers(next)
	if err != nil {
		return api.Policy{}, nil, 0, err
	}
	return pol, cleaned, removed, nil
}

// DropOnceRules strips single-run and expired rules when a session restarts.
func DropOnceRules(layers []api.Layer, now time.Time) (api.Policy, []api.Layer, error) {
	return egress.DropOnceRules(layers, now)
}

func cloneLayers(in []api.Layer) []api.Layer { return api.CloneLayers(in) }

func (b *bridge) handlePolicy(req proto.Request) bool {
	switch req.Op {
	case "policy.ls", "policy.add", "policy.rm", "policy.log":
	default:
		return false
	}
	if err := CheckCaller(); err != nil {
		b.reply(req.ID, nil, err)
		return true
	}
	v, err := dispatchPolicyOp(req.Op, req.Args)
	b.reply(req.ID, v, err)
	return true
}

func dispatchPolicyOp(op string, rawArgs json.RawMessage) (any, error) {
	var base struct {
		ID      string `json:"id"`
		Session string `json:"session"`
	}
	if len(rawArgs) > 0 && json.Unmarshal(rawArgs, &base) != nil {
		return nil, UserError("bad_request", op+" request is not valid JSON", "Update the client.")
	}
	sid := base.Session
	if sid == "" {
		sid = base.ID
	}
	if !validID(sid) {
		return nil, UserError("not_found", "no session "+sid, "List sessions to find its ID.")
	}
	if op == "policy.log" {
		var args api.PolicyLogArgs
		if len(rawArgs) > 0 && json.Unmarshal(rawArgs, &args) != nil {
			return nil, UserError("bad_request", "invalid policy.log arguments", "Update the client.")
		}
		if args.Page {
			// A new bridge may serve a session whose older supervisor does
			// not know pages. The trusted host owns this file and reader.
			return readSessionAuditPage(sid, args)
		}
		if args.Cursor != "" || args.Limit != 0 {
			return nil, badAuditPageArgs()
		}
	}
	if alive(sid) {
		var fwd any
		if len(rawArgs) > 0 {
			fwd = rawArgs
		}
		if op == "policy.add" {
			var args api.PolicyAddArgs
			if json.Unmarshal(rawArgs, &args) != nil {
				return nil, UserError("bad_request", "invalid policy.add arguments", "Update the client.")
			}
			var features []string
			if args.Methods != nil {
				features = append(features, api.FeatureHTTPMethods)
			}
			if len(args.Tools) != 0 {
				features = append(features, api.FeatureModelTools)
			}
			if len(features) != 0 {
				return HolderRequestWithFeatures(sid, op, fwd, 5*time.Second, features...)
			}
		}
		return HolderRequest(sid, op, fwd, 5*time.Second)
	}
	return offlinePolicyOp(sid, op, rawArgs)
}

func readSessionAuditPage(sid string, args api.PolicyLogArgs) (any, error) {
	folder, err := os.Lstat(paths.SessionDir(sid))
	if err != nil || !folder.IsDir() || !auditOwnedInfo(folder) {
		return nil, UserError("audit_read", "cannot read audit from an untrusted session folder", "Check the session folder's ownership and permissions.")
	}
	manifest, err := LoadManifest(sid)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, UserError("not_found", "no session "+sid, "List sessions to find its ID.")
		}
		return nil, UserError("policy_read", "cannot read the session's policy", "Check that the session manifest is readable and valid, then try again.")
	}
	if manifest.ID != sid {
		return nil, UserError("policy_read", "the session record does not match its folder", "Check the session record before reading its audit.")
	}
	if manifest.Wall == nil {
		return nil, UserError("not_walled", "the session has no sandbox policy", "Start a walled session to use policy commands.")
	}
	args.ID = sid
	return ReadPolicyLog(paths.AuditLog(sid), args)
}

func offlinePolicyOp(sid, op string, rawArgs json.RawMessage) (any, error) {
	unlock, err := lockSession(sid)
	if err != nil {
		var pe *proto.Error
		if errors.As(err, &pe) {
			return nil, pe
		}
		return nil, UserError("policy_lock", "cannot lock the session for a policy command", "Check access to the session folder, then try again.")
	}
	defer unlock()
	m, err := LoadManifest(sid)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, UserError("not_found", "no session "+sid, "List sessions to find its ID.")
		}
		return nil, UserError("policy_read", "cannot read the session's policy", "Check that the session manifest is readable and valid, then try again.")
	}
	if m.Wall == nil {
		return nil, UserError("not_walled", "session "+sid+" has no sandbox policy", "Start a walled session to use policy commands.")
	}
	switch op {
	case "policy.ls":
		return PolicySnapshot(api.CloneLayers(m.Layers))
	case "policy.log":
		var a api.PolicyLogArgs
		if len(rawArgs) > 0 && json.Unmarshal(rawArgs, &a) != nil {
			return nil, UserError("bad_request", "invalid policy.log arguments", "Update the client.")
		}
		a.ID = sid
		return ReadPolicyLog(paths.AuditLog(sid), a)
	case "policy.add":
		var a api.PolicyAddArgs
		if err := json.Unmarshal(rawArgs, &a); err != nil {
			return nil, UserError("bad_request", "invalid policy.add arguments", "Update the client.")
		}
		pol, layers, rule, err := ApplyPolicyAdd(m.Layers, a)
		if err != nil {
			return nil, err
		}
		m.Policy, m.Layers = &pol, layers
		return rule, saveOfflinePolicy(m)
	case "policy.rm":
		var a api.PolicyRmArgs
		if err := json.Unmarshal(rawArgs, &a); err != nil {
			return nil, UserError("bad_request", "invalid policy.rm arguments", "Update the client.")
		}
		pol, layers, removed, err := ApplyPolicyRm(m.Layers, a)
		if err != nil {
			return nil, err
		}
		m.Policy, m.Layers = &pol, layers
		return map[string]int{"removed": removed}, saveOfflinePolicy(m)
	}
	return nil, UserError("unknown_op", "unknown policy op "+op, "Update the client.")
}

func saveOfflinePolicy(m *Manifest) error {
	if err := m.Save(); err != nil {
		return UserError("policy_save", "cannot save the session's policy", "Check that the session folder is writable, then try again.")
	}
	return nil
}
