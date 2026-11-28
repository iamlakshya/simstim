// Package egress makes every network connection and DNS answer for one walled
// session. It runs on the trusted side of the host without listening on any
// network port (decisions 0030 and 0031), enforces a flat deny-wins TOML
// policy, refuses private and special addresses, verifies TLS SNI and refuses
// ECH via crypto/tls, and emits one audit line per flow.
package egress

import (
	"embed"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/BurntSushi/toml"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
)

//go:embed rules/presets.toml rules/ranges.toml
var rulesFS embed.FS

// Preset names the built-in network preset of a walled session.
type Preset string

const (
	// PresetLocked allows only the agent's own Egress hosts on port 443.
	PresetLocked Preset = "locked"
	// PresetBalanced allows locked hosts plus common package registries and code hosts on port 443.
	PresetBalanced Preset = "balanced"
	// PresetOpen allows every public host outside refused ranges and locked or explicit denies.
	PresetOpen Preset = "open"
)

// Action is the policy decision for a flow or DNS query.
type Action string

const (
	// ActionAllow permits the flow or DNS query.
	ActionAllow Action = "allow"
	// ActionDeny refuses the flow or DNS query.
	ActionDeny Action = "deny"
)

// Mode says how an allowed flow is carried.
type Mode string

const (
	// ModeSplice forwards raw TCP bytes after the SNI check on port 443.
	ModeSplice Mode = "splice"
	// ModeHTTP forwards an absolute-form plain HTTP proxy request.
	ModeHTTP Mode = "http"
	// ModeGateway proxies a plain-HTTP model API route over verified upstream TLS.
	ModeGateway Mode = "gateway"
)

// UnknownAction decides what happens when no rule matches a flow.
type UnknownAction string

const (
	// UnknownDeny refuses unmatched flows immediately (the v1 default).
	UnknownDeny UnknownAction = "deny"
	// UnknownAsk refuses unmatched flows immediately with the grant hint and,
	// in synthetic DNS mode, assigns a synthetic 198.18.0.0/15 IP without
	// querying upstream so the transparent flow reaches the policy engine.
	UnknownAsk UnknownAction = "ask"
	// UnknownAllow permits unmatched flows and logs them as "allow-unknown".
	UnknownAllow UnknownAction = "allow"
)

// Request is one network flow or HTTP request for the policy engine to decide.
type Request struct {
	Host   string
	Port   int
	Method string
	Path   string
	Header http.Header
}

// Decision is the result of evaluating a Request against the session policy.
type Decision struct {
	Action Action
	Mode   Mode
	Rule   string
	Why    string
}

// Config is the TOML policy document for one walled session.
// Any deny rule beats any allow rule; locked denies cannot be overridden by
// live session grants.
type Config struct {
	Session string        `toml:"session"`
	Agent   string        `toml:"agent"`
	Sandbox SandboxConfig `toml:"sandbox"`
	Network NetworkConfig `toml:"network"`
	Lock    LockConfig    `toml:"lock"`
}

// SandboxConfig holds sandbox-level policy fields used by the egress process.
type SandboxConfig struct {
	Preset          Preset `toml:"preset"`
	PackageManagers string `toml:"package_managers"`
}

// NetworkConfig holds the session's allowlist, denylist, budgets and unknown action.
type NetworkConfig struct {
	Allow         []string            `toml:"allow"`
	Deny          []string            `toml:"deny"`
	Unknown       UnknownAction       `toml:"unknown"`
	ModelTools    map[string][]string `toml:"model_tools"`
	MinReleaseAge string              `toml:"min_release_age"`
	ModelBudget   int64               `toml:"model_budget"`
	UploadBudget  string              `toml:"upload_budget"`
}

// LockConfig holds denies that no live session grant can override.
// Both `[lock] deny = [...]` and `[lock] network.deny = [...]` are accepted.
type LockConfig struct {
	Deny    []string          `toml:"deny"`
	Network LockNetworkConfig `toml:"network"`
}

// LockNetworkConfig matches `[lock] network.deny = [...]` from spec 8.3.
type LockNetworkConfig struct {
	Deny []string `toml:"deny"`
}

// ParsePolicy decodes a TOML session policy strictly, rejecting unknown keys.
func ParsePolicy(data []byte) (Config, error) {
	var cfg Config
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("egress: parse policy TOML: %w. Try: check the TOML syntax in the session policy", err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return Config{}, fmt.Errorf("egress: unknown policy key %q. Try: remove it or check %s config check", strings.Join(keys, ", "), brand.Name)
	}
	return cfg, nil
}

type policySnapshot struct {
	session      string
	agent        string
	preset       Preset
	unknown      UnknownAction
	locks        []rule
	denies       []rule
	allows       []rule
	grants       []rule // live grants added at runtime
	agentRules   []rule
	presetList   []rule
	gatewayHosts map[string]struct{}
	modelTools   map[string]map[string]bool
	modelBudget  int64
	uploadBudget int64
}

// Engine evaluates network flows and DNS queries against a flat deny-wins
// session policy. All methods are safe for concurrent use; live updates swap
// the immutable policy snapshot atomically.
type Engine struct {
	mu   sync.Mutex // serializes live mutations (Allow, Deny, Reload)
	snap atomic.Pointer[policySnapshot]
}

// NewEngine compiles cfg into a policy Engine.
func NewEngine(cfg Config) (*Engine, error) {
	s, err := compileSnapshot(cfg, nil)
	if err != nil {
		return nil, err
	}
	e := &Engine{}
	e.snap.Store(s)
	return e, nil
}

func compileSnapshot(cfg Config, grants []rule) (*policySnapshot, error) {
	preset := cfg.Sandbox.Preset
	if preset == "" {
		preset = PresetBalanced
	}
	switch preset {
	case PresetLocked, PresetBalanced, PresetOpen:
	default:
		return nil, fmt.Errorf("egress: unknown preset %q. Try: use locked, balanced or open", preset)
	}
	unknown := cfg.Network.Unknown
	if unknown == "" {
		unknown = UnknownDeny
	}
	switch unknown {
	case UnknownDeny, UnknownAsk, UnknownAllow:
	default:
		return nil, fmt.Errorf("egress: unknown network.unknown action %q. Try: use deny, ask or allow", unknown)
	}
	if _, err := api.ParsePackageManagers(cfg.Sandbox.PackageManagers); err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}
	if _, _, err := api.ParseMinReleaseAge(cfg.Network.MinReleaseAge); err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}
	if err := api.CheckModelBudget(cfg.Network.ModelBudget); err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}
	uploadBudgetBytes, _, err := api.ParseUploadBudget(cfg.Network.UploadBudget)
	if err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}

	presets, err := builtinPresets()
	if err != nil {
		return nil, err
	}

	lockInputs := append(append([]string(nil), cfg.Lock.Deny...), cfg.Lock.Network.Deny...)
	locks, err := parseRules(lockInputs, true)
	if err != nil {
		return nil, fmt.Errorf("egress: lock rule: %w", err)
	}
	denies, err := parseRules(cfg.Network.Deny, true)
	if err != nil {
		return nil, fmt.Errorf("egress: deny rule: %w", err)
	}
	allows, err := parseRules(cfg.Network.Allow, false)
	if err != nil {
		return nil, fmt.Errorf("egress: allow rule: %w", err)
	}
	agentRules, err := compileAgentEgress(cfg.Agent)
	if err != nil {
		return nil, err
	}
	gwHosts, err := compileGatewayHosts(nil)
	if err != nil {
		return nil, err
	}
	modelTools, err := compileModelTools(cfg.Network.ModelTools)
	if err != nil {
		return nil, err
	}

	return &policySnapshot{
		session:      cfg.Session,
		agent:        cfg.Agent,
		preset:       preset,
		unknown:      unknown,
		locks:        locks,
		denies:       denies,
		allows:       allows,
		grants:       append([]rule(nil), grants...),
		agentRules:   agentRules,
		presetList:   presets[preset],
		gatewayHosts: gwHosts,
		modelTools:   modelTools,
		modelBudget:  cfg.Network.ModelBudget,
		uploadBudget: uploadBudgetBytes,
	}, nil
}

func compileAgentEgress(kind string) ([]rule, error) {
	if kind == "" {
		return nil, nil
	}
	a, ok := agents.Find(kind)
	if !ok {
		return nil, fmt.Errorf("egress: unknown agent %q. Try: check %s agents", kind, brand.Name)
	}
	out := make([]rule, 0, len(a.Egress))
	for _, entry := range a.Egress {
		host := entry
		if i := strings.IndexByte(host, '/'); i >= 0 {
			host = host[:i]
		}
		r, err := parseRule(host, false)
		if err != nil {
			return nil, fmt.Errorf("egress: agent %q host %q: %w", kind, entry, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// Reload replaces the policy atomically while preserving existing locked denies
// and clearing session grants.
func (e *Engine) Reload(cfg Config) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := e.snap.Load()
	s, err := compileSnapshot(cfg, nil)
	if err != nil {
		return err
	}
	if len(cur.locks) > 0 {
		merged := make([]rule, 0, len(cur.locks)+len(s.locks))
		merged = append(merged, cur.locks...)
		merged = append(merged, s.locks...)
		s.locks = merged
	}
	for h := range cur.gatewayHosts {
		s.gatewayHosts[h] = struct{}{}
	}
	e.snap.Store(s)
	return nil
}

// ErrPolicyLocked is returned by Engine.Allow when a locked deny covers the
// requested target.
var ErrPolicyLocked = errors.New("policy locked")

// Allow adds a live session allow rule atomically. It refuses targets covered
// by a locked deny or registered model gateway hosts.
func (e *Engine) Allow(target string) error {
	r, err := parseRule(target, false)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := e.snap.Load()
	if locked, pat := matchesAnyLock(cur.locks, r); locked {
		return fmt.Errorf("egress: %w: %q is locked by %q. Try: remove the lock from config before allowing it", ErrPolicyLocked, target, pat)
	}
	if r.kind == ruleExactHost && cur.isGatewayHost(r.host) {
		return fmt.Errorf("egress: %w: %s", ErrPolicyLocked, gatewayDirectDenyHint(r.host))
	}
	next := *cur
	next.grants = append(append([]rule(nil), cur.grants...), r)
	e.snap.Store(&next)
	return nil
}

// Deny adds a live session deny rule atomically. Under deny-wins semantics it
// overrides any allow rule, preset or agent host for new flows.
func (e *Engine) Deny(target string) error {
	r, err := parseRule(target, true)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := e.snap.Load()
	next := *cur
	next.denies = append(append([]rule(nil), cur.denies...), r)
	e.snap.Store(&next)
	return nil
}

// Session returns the session display label used in 403 hints.
func (e *Engine) Session() string {
	return e.snap.Load().session
}

// Unknown returns the configured unknown-site action.
func (e *Engine) Unknown() UnknownAction {
	return e.snap.Load().unknown
}

// Decide evaluates req under flat deny-wins policy semantics.
func (e *Engine) Decide(req Request) Decision {
	host, err := normalizeHost(req.Host)
	if err != nil {
		return Decision{Action: ActionDeny, Rule: "invalid", Why: err.Error()}
	}
	if req.Port < 1 || req.Port > 65535 {
		return Decision{Action: ActionDeny, Rule: "invalid", Why: fmt.Sprintf("invalid port %d", req.Port)}
	}
	s := e.snap.Load()
	if r, ok := matchRule(s.locks, host, req.Port); ok {
		return Decision{Action: ActionDeny, Rule: "lock:" + r.raw, Why: "denied by lock"}
	}
	if r, ok := matchRule(s.denies, host, req.Port); ok {
		return Decision{Action: ActionDeny, Rule: "deny:" + r.raw, Why: "denied by policy"}
	}
	if s.isGatewayHost(host) {
		return Decision{Action: ActionDeny, Rule: "gateway:" + host, Why: gatewayDirectDenyHint(host)}
	}
	if r, ok := matchRule(s.allows, host, req.Port); ok {
		return Decision{Action: ActionAllow, Mode: ModeSplice, Rule: "allow:" + r.raw}
	}
	if r, ok := matchRule(s.grants, host, req.Port); ok {
		return Decision{Action: ActionAllow, Mode: ModeSplice, Rule: "grant:" + r.raw}
	}
	if _, ok := matchRule(s.agentRules, host, req.Port); ok {
		return Decision{Action: ActionAllow, Mode: ModeSplice, Rule: "agent:" + s.agent}
	}
	if s.preset == PresetOpen {
		return Decision{Action: ActionAllow, Mode: ModeSplice, Rule: "preset:open"}
	}
	if _, ok := matchRule(s.presetList, host, req.Port); ok {
		return Decision{Action: ActionAllow, Mode: ModeSplice, Rule: "preset:" + string(s.preset)}
	}
	if s.unknown == UnknownAllow {
		return Decision{Action: ActionAllow, Mode: ModeSplice, Rule: "unknown:allow", Why: "allowed by unknown=allow"}
	}
	if s.unknown == UnknownAsk {
		return Decision{Action: ActionDeny, Rule: "unknown:ask", Why: e.DenyHint(host, req.Port)}
	}
	return Decision{Action: ActionDeny, Rule: "unknown:deny", Why: e.DenyHint(host, req.Port)}
}

// DecideDNS reports whether DNS resolution is permitted for host.
// Denied and unknown (when unknown=deny) names return ActionDeny so no query
// leaves the host.
func (e *Engine) DecideDNS(rawHost string) Decision {
	host, err := normalizeHost(rawHost)
	if err != nil {
		return Decision{Action: ActionDeny, Rule: "invalid", Why: err.Error()}
	}
	s := e.snap.Load()
	if r, ok := matchHostOnlyDeny(s.locks, host); ok {
		return Decision{Action: ActionDeny, Rule: "lock:" + r.raw, Why: "denied by lock"}
	}
	if r, ok := matchHostOnlyDeny(s.denies, host); ok {
		return Decision{Action: ActionDeny, Rule: "deny:" + r.raw, Why: "denied by policy"}
	}
	if s.isGatewayHost(host) {
		return Decision{Action: ActionDeny, Rule: "gateway:" + host, Why: "model host uses gateway route"}
	}
	if r, ok := matchAllowedHost(s, s.allows, host); ok {
		return Decision{Action: ActionAllow, Rule: "allow:" + r.raw}
	}
	if r, ok := matchAllowedHost(s, s.grants, host); ok {
		return Decision{Action: ActionAllow, Rule: "grant:" + r.raw}
	}
	if _, ok := matchAllowedHost(s, s.agentRules, host); ok {
		return Decision{Action: ActionAllow, Rule: "agent:" + s.agent}
	}
	if s.preset == PresetOpen {
		return Decision{Action: ActionAllow, Rule: "preset:open"}
	}
	if _, ok := matchAllowedHost(s, s.presetList, host); ok {
		return Decision{Action: ActionAllow, Rule: "preset:" + string(s.preset)}
	}
	if s.unknown == UnknownAllow {
		return Decision{Action: ActionAllow, Rule: "unknown:allow"}
	}
	if s.unknown == UnknownAsk {
		return Decision{Action: ActionDeny, Rule: "unknown:ask", Why: "unknown host requires approval"}
	}
	return Decision{Action: ActionDeny, Rule: "unknown:deny", Why: "not allowed by policy"}
}

// AllowExplicitIP reports whether addr was explicitly granted by IP or CIDR in
// the session policy (and is not denied by a lock or deny rule).
func (e *Engine) AllowExplicitIP(addr netip.Addr, port int) bool {
	if !addr.IsValid() {
		return false
	}
	clean := addr.WithZone("").Unmap()
	host := clean.String()
	s := e.snap.Load()
	if _, ok := matchRule(s.locks, host, port); ok {
		return false
	}
	if _, ok := matchRule(s.denies, host, port); ok {
		return false
	}
	for _, list := range [][]rule{s.allows, s.grants} {
		for _, r := range list {
			if r.explicitIP && r.matches(host, port) {
				return true
			}
		}
	}
	return false
}
