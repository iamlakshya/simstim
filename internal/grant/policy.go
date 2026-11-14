// Package grant exposes the canonical policy engine to policy front ends.
// Target syntax, matching and once-grant state remain owned by that engine.
package grant

import (
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/nettarget"
	"github.com/iamlakshya/simstim/internal/proto"
)

// CheckHost validates and classifies a network target. A private destination
// is valid syntax; the client's grant gate decides whether its host may get it.
func CheckHost(raw string) (string, bool, error) { return nettarget.Classify(raw) }

// MergeLayers delegates to the single canonical merge used by the supervisor.
func MergeLayers(layers []api.Layer) (api.Policy, []api.Layer, error) {
	return egress.MergeLayers(layers)
}

// Explanation identifies the rule or fallback that decides a diagnostic check.
type Explanation struct {
	Source  api.LayerSource `json:"source,omitempty"`
	Allow   bool            `json:"allow"`
	Rule    string          `json:"rule,omitempty"`
	Layer   api.LayerKind   `json:"layer,omitempty"`
	Name    string          `json:"name,omitempty"`
	Unknown api.Unknown     `json:"unknown,omitempty"`
	Applies string          `json:"applies"`
}

// Decide explains one concrete destination with a fresh diagnostic engine.
// It never reserves or consumes a running session's once grant.
func Decide(agent string, pol api.Policy, target string) (Explanation, error) {
	t, err := nettarget.Parse(target)
	if err != nil {
		return Explanation{}, err
	}
	if t.Wildcard || t.Prefix.IsValid() && t.Prefix.Bits() != t.Prefix.Addr().BitLen() {
		return Explanation{}, badCheckTarget()
	}
	host := t.Host
	if t.Prefix.IsValid() {
		host = t.Prefix.Addr().String()
	}
	port := t.Port
	if port == 0 {
		port = 443
	}
	engine, err := egress.NewEngine(agent, pol)
	if err != nil {
		return Explanation{}, err
	}
	decision := engine.Decide(egress.Request{Host: host, Port: port, Path: t.Path, Now: time.Now()})
	out := Explanation{Allow: decision.Allow, Rule: decision.Rule, Applies: "at_once"}
	if decision.Match != nil {
		out.Rule, out.Layer, out.Name, out.Source = decision.RuleID, decision.Match.Kind, decision.Match.Name, decision.Match.Source
		return out, nil
	}
	if pol.Preset == api.PresetOpen && decision.Allow {
		out.Layer, out.Name = api.LayerBuiltin, "open"
		return out, nil
	}
	if strings.HasPrefix(decision.Rule, "preset:") {
		out.Layer = api.LayerBuiltin
		out.Unknown = pol.Unknown
	}
	return out, nil
}

func badCheckTarget() error {
	return &proto.Error{Code: "bad_target", Message: "policy check needs one concrete host or IP address", Hint: "Pass a host or an IP address, with an optional port or path."}
}

// CheckAddition validates a proposed session rule against the canonical locks
// without touching a running engine or its once-grant ledger.
func CheckAddition(policy api.Policy, rule api.Rule) error {
	var layers []api.Layer
	for _, lock := range policy.Locks {
		copy := lock
		layers = append(layers, api.Layer{Kind: lock.Layer, Name: lock.Name, Lock: &copy})
	}
	layers = append(layers, api.Layer{Kind: api.LayerSession, Rules: []api.Rule{rule}})
	_, _, err := egress.MergeLayers(layers)
	return err
}
