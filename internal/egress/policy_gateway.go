package egress

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/brand"
)

func compileGatewayHosts(extra []string) (map[string]struct{}, error) {
	out := map[string]struct{}{}
	for _, h := range agents.ModelHosts() {
		norm, err := normalizeHost(h)
		if err != nil {
			return nil, fmt.Errorf("egress: model host %q: %w", h, err)
		}
		out[norm] = struct{}{}
	}
	for _, h := range extra {
		norm, err := normalizeHost(h)
		if err != nil {
			return nil, fmt.Errorf("egress: gateway host %q: %w", h, err)
		}
		out[norm] = struct{}{}
	}
	return out, nil
}

func compileModelTools(raw map[string][]string) (map[string]map[string]bool, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]map[string]bool, len(raw))
	for host, grants := range raw {
		norm, err := normalizeHost(host)
		if err != nil {
			return nil, fmt.Errorf("egress: model_tools host %q: %w", host, err)
		}
		set := out[norm]
		if set == nil {
			set = map[string]bool{}
			out[norm] = set
		}
		for _, g := range grants {
			name := strings.TrimSpace(g)
			if !ValidToolGrant(name) {
				return nil, fmt.Errorf("egress: unknown model tool grant %q for %s. Try: use web_fetch, web_search, code_execution, mcp, url_content or files", g, norm)
			}
			set[name] = true
		}
	}
	return out, nil
}

func (s *policySnapshot) isGatewayHost(host string) bool {
	if len(s.gatewayHosts) == 0 {
		return false
	}
	_, ok := s.gatewayHosts[host]
	return ok
}

func cloneModelTools(src map[string]map[string]bool) map[string]map[string]bool {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]map[string]bool, len(src))
	for k, v := range src {
		out[k] = maps.Clone(v)
	}
	return out
}

// Preset returns the session's active network preset (locked, balanced or open).
func (e *Engine) Preset() Preset {
	return e.snap.Load().preset
}

// ModelBudget returns the configured model token cap (0 for no limit).
func (e *Engine) ModelBudget() int64 {
	return e.snap.Load().modelBudget
}

// UploadBudget returns the configured per-host hourly upload byte cap (0 for no limit).
func (e *Engine) UploadBudget() int64 {
	return e.snap.Load().uploadBudget
}

// DecideGateway checks whether a model gateway route is permitted to dial its
// upstream host. Locks and explicit deny rules still beat the gateway (deny
// wins); otherwise the configured gateway route is allowed.
func (e *Engine) DecideGateway(rawHost string, port int) Decision {
	host, err := normalizeHost(rawHost)
	if err != nil {
		return Decision{Action: ActionDeny, Rule: "invalid", Why: err.Error()}
	}
	if port < 1 || port > 65535 {
		return Decision{Action: ActionDeny, Rule: "invalid", Why: fmt.Sprintf("invalid port %d", port)}
	}
	s := e.snap.Load()
	if r, ok := matchRule(s.locks, host, port); ok {
		return Decision{Action: ActionDeny, Rule: "lock:" + r.raw, Why: "denied by lock"}
	}
	if r, ok := matchRule(s.denies, host, port); ok {
		return Decision{Action: ActionDeny, Rule: "deny:" + r.raw, Why: "denied by policy"}
	}
	return Decision{Action: ActionAllow, Mode: ModeGateway, Rule: "gateway:" + host}
}

// RegisterGatewayHosts marks hosts as gateway-only so direct CONNECT, forward
// proxy HTTP and transparent splice to them are always refused, even under the
// open preset or explicit allow rules.
func (e *Engine) RegisterGatewayHosts(hosts []string) error {
	if len(hosts) == 0 {
		return nil
	}
	normed := make([]string, 0, len(hosts))
	for _, h := range hosts {
		n, err := normalizeHost(h)
		if err != nil {
			return fmt.Errorf("egress: gateway host %q: %w", h, err)
		}
		normed = append(normed, n)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := e.snap.Load()
	next := *cur
	next.gatewayHosts = maps.Clone(cur.gatewayHosts)
	if next.gatewayHosts == nil {
		next.gatewayHosts = map[string]struct{}{}
	}
	for _, h := range normed {
		next.gatewayHosts[h] = struct{}{}
	}
	e.snap.Store(&next)
	return nil
}

// ToolAllowed reports whether toolGrant (such as "web_fetch" or "mcp") has been
// granted for host in policy or via AllowTools.
func (e *Engine) ToolAllowed(rawHost, toolGrant string) bool {
	host, err := normalizeHost(rawHost)
	if err != nil {
		return false
	}
	s := e.snap.Load()
	return s.modelTools[host][toolGrant]
}

// AllowTools grants one or more spec 7.2 --tools names for host live.
func (e *Engine) AllowTools(rawHost string, tools []string) error {
	host, err := normalizeHost(rawHost)
	if err != nil {
		return fmt.Errorf("egress: model tool host %q: %w", rawHost, err)
	}
	if len(tools) == 0 {
		return errors.New("egress: no tool grants given. Try: pass one or more of web_fetch, web_search, code_execution, mcp, url_content, files")
	}
	cleaned := make([]string, 0, len(tools))
	for _, t := range tools {
		name := strings.TrimSpace(t)
		if !ValidToolGrant(name) {
			return fmt.Errorf("egress: unknown model tool grant %q. Try: use web_fetch, web_search, code_execution, mcp, url_content or files", t)
		}
		if !slices.Contains(cleaned, name) {
			cleaned = append(cleaned, name)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	cur := e.snap.Load()
	next := *cur
	next.modelTools = cloneModelTools(cur.modelTools)
	if next.modelTools == nil {
		next.modelTools = map[string]map[string]bool{}
	}
	if next.modelTools[host] == nil {
		next.modelTools[host] = map[string]bool{}
	}
	for _, name := range cleaned {
		next.modelTools[host][name] = true
	}
	e.snap.Store(&next)
	return nil
}

func gatewayDirectDenyHint(host string) string {
	return fmt.Sprintf("%s: direct CONNECT to model host %s is not allowed; use the model gateway route. Try: send model requests through the sandbox's gateway base URL instead of direct CONNECT", brand.Name, host)
}

// DenyHint formats the single-line 403 refusal message with its grant command.
func (e *Engine) DenyHint(host string, port int) string {
	target := host
	if port != 0 && port != 443 {
		target = net.JoinHostPort(host, strconv.Itoa(port))
	}
	sess := e.Session()
	if sess == "" {
		return fmt.Sprintf("%s: %s is not allowed. Try: %s policy allow %s", brand.Name, target, brand.Name, target)
	}
	return fmt.Sprintf("%s: %s is not allowed in %s. Try: %s policy allow %s --in %s", brand.Name, target, sess, brand.Name, target, sess)
}
