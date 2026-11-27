package egress

import (
	"net/netip"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/proto"
)

func canonicalRuleTools(rule api.Rule) ([]string, error) {
	tools, err := api.CanonicalModelTools(rule.Tools)
	if err != nil {
		return nil, modelGrantError(err.Error())
	}
	if len(tools) == 0 {
		return nil, nil
	}
	if !rule.Allow || rule.Layer != api.LayerSession || rule.Name != "" || rule.Source != "" || rule.Door != "" && rule.Door != api.DoorNet {
		return nil, modelGrantError("model capabilities require a session network allowance")
	}
	target, err := compileTarget(rule.Target, true)
	if err != nil || target.wildcard || target.isNumeric || target.path != "" || target.port != 0 && target.port != 443 || rule.Mode != "" {
		return nil, modelGrantError("model capabilities require one exact model host on port 443")
	}
	kind, spliceOnly, err := httpClass(target.host)
	if err != nil || spliceOnly || kind != "model" {
		return nil, modelGrantError("model capabilities require a guarded model host")
	}
	return tools, nil
}

func modelGrantError(message string) error {
	return &proto.Error{Code: "bad_tools", Message: message,
		Hint: "Use policy allow <model-host> --tools <capability> --in <host>/<session>."}
}

func (e *Engine) addModelAgent(kind string) {
	if agent, ok := agents.Find(kind); ok {
		if guard := agent.ModelPolicy(); guard != nil {
			e.modelGuards = append(e.modelGuards, *guard)
		}
	}
}

func (e *Engine) modelGuard(request Request) *agents.ModelGuard {
	host, err := NormaliseHost(request.Host)
	if err != nil {
		return nil
	}
	request.Host = host
	for i := range e.modelGuards {
		guard := &e.modelGuards[i]
		if slices.Contains(guard.Hosts, request.Host) {
			return guard
		}
	}
	return agents.ModelHostPolicy(request.Host)
}

func (e *Engine) evaluateModelNetwork(request Request, permit *Permit, mode evalMode) Decision {
	if e.ledger == nil {
		return Decision{Rule: "invalid"}
	}
	e.ledger.mu.Lock()
	defer e.ledger.mu.Unlock()
	return e.evaluateModelNetworkLocked(request, permit, mode)
}

func (e *Engine) evaluateModelNetworkLocked(request Request, permit *Permit, mode evalMode) Decision {
	kind, _, _ := httpClass(request.Host)
	if kind == "model" {
		baseline := request
		// Keep permanent and already-owned tool grants visible. Only an unowned
		// capability-bearing once slot waits behind ordinary network authority.
		baseline.withoutModelTools = true
		check := evalPrecheck
		if mode == evalRevalidate {
			check = evalRevalidate
		}
		if decision := e.evaluateHTTPLocked(baseline, permit, check); decision.Allow {
			return e.evaluateHTTPLocked(baseline, permit, mode)
		}
	}
	return e.evaluateHTTPLocked(request, permit, mode)
}

func canonicalModelRequest(request Request) (Request, bool) {
	host, err := NormaliseHost(request.Host)
	if err != nil || request.Port < 1 || request.Port > 65535 {
		return Request{}, false
	}
	path, err := canonicalPath(request.Path)
	if err != nil {
		return Request{}, false
	}
	request.Host, request.Path = host, path
	if path == "" {
		request.Method = ""
	}
	return request, true
}

// This facade retains the one atomic admission and every ordinary deny/lock.
// It cannot reserve a capability before discovering an invalid path or deny.
func (e *Engine) modelToolAllowed(request Request, tool string, permit *Permit, mode evalMode) bool {
	model := modelRequest{request: request, features: []string{tool}}
	return e.admitModelRequest(&model, permit, false, mode) == ""
}

func (e *Engine) modelToolAllowedLocked(request Request, tool string, permit *Permit, mode evalMode) bool {
	at := request.Now
	if at.IsZero() {
		at = e.now()
	}
	for _, lock := range e.locks {
		if matchesRuleMethod(lock, request.Method) && e.matchesLocked(lock, request.Host, netip.Addr{}, false, request.Port, request.Path, at) {
			return false
		}
	}
	for _, layer := range e.layers {
		allow, denied := e.choose(layer.rules, permit, mode, func(rule compiledRule) bool {
			return (!rule.allow || slices.Contains(rule.tools, tool)) && matchesRuleMethod(rule, request.Method) && e.matchesLocked(rule, request.Host, netip.Addr{}, false, request.Port, request.Path, at)
		})
		if denied != nil {
			return false
		}
		if allow != nil {
			e.useOnce(allow, permit, mode)
			return true
		}
	}
	return false
}

func modelToolCapability(wire, kind string) string {
	switch {
	case wire == "anthropic-messages" && strings.HasPrefix(kind, "web_fetch_"):
		return "web_fetch"
	case wire == "anthropic-messages" && strings.HasPrefix(kind, "code_execution_") || wire != "anthropic-messages" && kind == "code_interpreter":
		return "code_execution"
	case kind == "mcp" || kind == "mcp_toolset":
		return "mcp"
	default:
		return ""
	}
}
