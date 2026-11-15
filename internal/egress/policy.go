package egress

import (
	_ "embed"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

//go:embed rules/presets.toml
var presetsTOML string

type presetsFile struct {
	Balanced struct {
		Registries []presetEntry `toml:"registries"`
		CodeHosts  []presetEntry `toml:"code_hosts"`
	} `toml:"balanced"`
}

type presetEntry struct {
	Host   string `toml:"host"`
	Ports  []int  `toml:"ports"`
	Source string `toml:"source"`
}

var loadPresets = sync.OnceValues(func() ([]compiledRule, error) {
	var f presetsFile
	md, err := toml.Decode(presetsTOML, &f)
	if err != nil {
		return nil, fmt.Errorf("decode presets.toml: %w", err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return nil, fmt.Errorf("unknown keys in presets.toml: %v", u)
	}
	all := append(slices.Clone(f.Balanced.Registries), f.Balanced.CodeHosts...)
	if len(all) == 0 {
		return nil, errors.New("presets.toml has no balanced entries")
	}
	var out []compiledRule
	for _, e := range all {
		if !strings.HasPrefix(e.Source, "https://") {
			return nil, fmt.Errorf("preset host %q lacks an https source link", e.Host)
		}
		h, err := NormaliseHost(e.Host)
		if err != nil {
			return nil, fmt.Errorf("preset host %q: %w", e.Host, err)
		}
		if len(e.Ports) == 0 {
			return nil, fmt.Errorf("preset host %q names no ports", e.Host)
		}
		for _, p := range e.Ports {
			if p < 1 || p > 65535 {
				return nil, fmt.Errorf("preset host %q has invalid port %d", e.Host, p)
			}
			out = append(out, compiledRule{id: "preset:balanced", allow: true, host: h, port: p, ref: api.LayerRef{Kind: api.LayerBuiltin, Name: "balanced"}})
		}
	}
	return out, nil
})

// Request describes a network flow or HTTP request to evaluate against policy.
type Request struct {
	door              api.Door // internal typed dimension; network callers leave it empty
	scheme            string
	query             string // immutable query of an inspected HTTP request
	forceQuery        bool
	withoutModelTools bool // internal probe through the same policy matcher
	Host              string
	Port              int
	Method            string
	Path              string // The visible URL path. Empty means the flow is opaque.
	Now               time.Time
}

// Decision is the policy engine's verdict for one Request.
type Decision struct {
	// Match records the exact matched source without parsing diagnostic labels.
	Match  *api.LayerRef
	RuleID string
	// Target is the canonical matched target. Empty means a preset fallback.
	// It allows class policy to distinguish an explicit path from a host grant.
	Target string

	Allow bool
	Rule  string
	// unmatched marks a terminal preset fallback with no eligible rule.
	// Diagnostic rule labels never determine this internal state.
	unmatched   bool
	httpRefusal httpRefusalKind
}

type compiledRule struct {
	door       api.Door
	ref        api.LayerRef
	id         string
	allow      bool
	host       string
	wildcard   bool
	isNumeric  bool
	prefix     netip.Prefix
	port       int // 0 means default: 443 on allow, any port on deny
	path       string
	pathPrefix bool
	tools      []string
	methods    *[]string
	once       bool
	until      time.Time
	grantID    string
	slot       *onceSlot
}

type compiledLayer struct {
	kind   api.LayerKind
	name   string
	source api.LayerSource
	rules  []compiledRule
}

// Engine evaluates network flows against locks, merged layers, the agent's
// Egress hosts and the active preset (spec 7.2 and 8.2).
type Engine struct {
	now         func() time.Time
	modelGuards []agents.ModelGuard
	agentKind   string
	agentRules  []compiledRule
	balanced    []compiledRule
	preset      api.Preset
	unknown     api.Unknown
	locks       []compiledRule
	layers      []compiledLayer // narrowest first
	ledger      *onceLedger
}

// NewEngine builds a policy engine for agentKind from a merged Policy.
func NewEngine(agentKind string, pol api.Policy) (*Engine, error) {
	agentRules, err := compileAgentEgress(agentKind)
	if err != nil {
		return nil, err
	}
	engine, err := newEngineWithAgentRules(agentKind, agentRules, pol)
	if err == nil {
		engine.addModelAgent(agentKind)
	}
	return engine, err
}

func newEngineWithAgentRules(agentKind string, agentRules []compiledRule, pol api.Policy) (*Engine, error) {
	rules, err := CanonicalRuleInstances(pol.Rules)
	if err != nil {
		return nil, err
	}
	pol.Rules = rules
	balanced, err := loadPresets()
	if err != nil {
		return nil, err
	}
	preset, err := api.ParsePreset(string(pol.Preset))
	if err != nil {
		return nil, &proto.Error{Code: "bad_request", Message: err.Error(), Hint: "Pass --preset locked, balanced or open."}
	}
	unknown, err := api.ParseUnknown(string(pol.Unknown))
	if err != nil {
		return nil, &proto.Error{Code: "bad_request", Message: err.Error(), Hint: "Set network.unknown to ask, deny, allow or classify."}
	}
	locks, err := compilePolicyLocks(pol.Locks)
	if err != nil {
		return nil, err
	}
	layers, err := compilePolicyLayers(pol)
	if err != nil {
		return nil, err
	}
	eng := &Engine{
		now:        time.Now,
		agentKind:  agentKind,
		agentRules: agentRules,
		balanced:   balanced,
		preset:     preset,
		unknown:    unknown,
		locks:      locks,
		layers:     layers,
		ledger:     newOnceLedger(),
	}
	eng.ledger.syncPolicy(eng)
	return eng, nil
}

func compileAgentEgress(kind string) ([]compiledRule, error) {
	a, ok := agents.Find(kind)
	if !ok || len(a.Egress) == 0 {
		return nil, nil
	}
	out := make([]compiledRule, 0, len(a.Egress))
	for _, entry := range a.Egress {
		cr, err := compileTarget(entry, true)
		if err != nil {
			return nil, fmt.Errorf("agent %s egress %q: %w", kind, entry, err)
		}
		cr.id = "agent:" + kind
		cr.ref = api.LayerRef{Kind: api.LayerBuiltin, Name: kind}
		if cr.port == 0 {
			cr.port = 443
		}
		out = append(out, cr)
	}
	return out, nil
}

// Decide evaluates req against locks, layers, the agent's Egress hosts and the
// session's preset (spec 7.2 and 8.2).
func (e *Engine) Decide(req Request) Decision {
	return e.evaluate(req, nil, evalStandalone)
}

func (e *Engine) decidePreset(host string, addr netip.Addr, isIP bool, port int, path string, at time.Time) Decision {
	switch e.preset {
	case api.PresetOpen:
		if !isIP {
			return Decision{Allow: true, Rule: "preset:open", unmatched: true}
		}
		return Decision{Allow: false, Rule: "preset:open", unmatched: true}
	case api.PresetBalanced:
		for _, r := range e.balanced {
			if e.matchesLocked(r, host, addr, isIP, port, path, at) {
				return Decision{Allow: true, Rule: r.id, RuleID: r.id, Target: formatCompiledTarget(r), Match: &r.ref}
			}
		}
		return Decision{Allow: false, Rule: "preset:balanced", unmatched: true}
	default:
		return Decision{Allow: false, Rule: "preset:locked", unmatched: true}
	}
}

// AllowsAddr reports whether an explicit numeric IP or CIDR allow rule wins
// for addr on port. Hostname grants and presets never open refused ranges.
func (e *Engine) AllowsAddr(addr netip.Addr, port int) bool {
	return e.evaluateAddr(addr, port, nil, evalStandalone)
}

func (e *Engine) matchesLocked(r compiledRule, host string, addr netip.Addr, isIP bool, port int, path string, at time.Time) bool {
	if !r.until.IsZero() && !at.Before(r.until) {
		return false
	}
	wantPort := r.port
	if wantPort == 0 && r.allow {
		wantPort = 443
	}
	if wantPort != 0 && wantPort != port {
		return false
	}
	if !matchesRulePath(r, path) {
		return false
	}
	if r.isNumeric {
		return isIP && (r.prefix.Contains(addr) || r.prefix.Contains(addr.Unmap()))
	}
	if isIP {
		return false
	}
	if r.wildcard {
		return len(host) > len(r.host)+1 && strings.HasSuffix(host, "."+r.host)
	}
	return host == r.host
}

func matchesRulePath(r compiledRule, path string) bool {
	if r.path == "" {
		return true
	}
	if path == "" {
		return !r.allow
	}
	if r.pathPrefix {
		return strings.HasPrefix(path, r.path)
	}
	return path == r.path
}
