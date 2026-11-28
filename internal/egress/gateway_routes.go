package egress

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
)

func compileGatewayRoute(rc GatewayRouteConfig) (*gatewayRoute, error) {
	slug := normalizeRouteSlug(rc.Route)
	if slug == "" {
		return nil, fmt.Errorf("egress gateway: invalid route slug %q. Try: use a single path segment such as anthropic or openai", rc.Route)
	}
	u, err := url.Parse(strings.TrimSpace(rc.Upstream))
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("egress gateway: route %q upstream %q must be an https URL. Try: use an origin such as https://api.anthropic.com", slug, rc.Upstream)
	}
	host, err := normalizeHost(u.Hostname())
	if err != nil {
		return nil, fmt.Errorf("egress gateway: route %q host: %w", slug, err)
	}
	port := 443
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("egress gateway: route %q has invalid port %q. Try: use a port between 1 and 65535", slug, p)
		}
	}
	return &gatewayRoute{
		slug:         slug,
		upstream:     u,
		upstreamHost: host,
		upstreamPort: port,
		guard:        rc.Guard,
	}, nil
}

// DefaultGatewayRoutes returns the built-in model gateway routes derived from
// the agent table (no agent-name branching). If agentKind names an agent with
// a ModelGateway, its route is included; otherwise all built-in agent routes
// are available so multi-agent sessions and shell checks work out of the box.
func DefaultGatewayRoutes(agentKind string) []GatewayRouteConfig {
	if agentKind != "" {
		if a, ok := agents.Find(agentKind); ok && a.ModelGateway != nil {
			return []GatewayRouteConfig{{
				Route:    a.ModelGateway.Route,
				Upstream: a.ModelGateway.Upstream,
				Guard:    a.ModelGuard,
			}}
		}
	}
	var out []GatewayRouteConfig
	seen := map[string]bool{}
	for _, a := range agents.Table {
		if a.ModelGateway == nil || seen[a.ModelGateway.Route] {
			continue
		}
		seen[a.ModelGateway.Route] = true
		out = append(out, GatewayRouteConfig{
			Route:    a.ModelGateway.Route,
			Upstream: a.ModelGateway.Upstream,
			Guard:    a.ModelGuard,
		})
	}
	return out
}

func stripGuestCredentials(h http.Header, extraHeader string) {
	for _, k := range []string{"Authorization", "X-Api-Key", "Proxy-Authorization", "Api-Key"} {
		h.Del(k)
	}
	if extraHeader != "" {
		h.Del(extraHeader)
	}
}

func isWebSocketUpgrade(r *http.Request) bool {
	for _, v := range r.Header.Values("Upgrade") {
		if strings.EqualFold(strings.TrimSpace(v), "websocket") {
			return true
		}
	}
	return false
}

func shouldInspectBody(method, cleanPath string, guard *agents.ModelGuard) bool {
	if guard == nil {
		return false
	}
	if !strings.EqualFold(method, http.MethodPost) &&
		!strings.EqualFold(method, http.MethodPut) &&
		!strings.EqualFold(method, http.MethodPatch) {
		return false
	}
	// File upload endpoints carry multipart form data rather than JSON.
	for _, f := range guard.Files {
		if matchFileEndpoint(f, method, cleanPath) {
			return false
		}
	}
	return true
}

func routeWire(route *gatewayRoute) string {
	if route != nil && route.guard != nil && route.guard.Wire != "" {
		return route.guard.Wire
	}
	return WireOpenAIResponses
}
