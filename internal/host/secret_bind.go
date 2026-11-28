package host

import (
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
)

// BoundRouteTarget is one resolved (route slug, upstream hostname) pair for a
// bound secret. Both host resolution and supervisor arming use BoundSecretRoutes
// so route slugs and host ownership can never diverge.
type BoundRouteTarget struct {
	Slug string
	Host string
}

// BoundSecretRoutes maps every destination host in b to its gateway route slug.
// It fails if explicit b.Route is paired with multiple hosts, disagrees with a
// built-in model gateway host's route, or if two hosts inside b derive the same
// route slug (for example "a-b.example" and "a.b.example").
func BoundSecretRoutes(b api.SecretBind) ([]BoundRouteTarget, error) {
	if b.Route != "" && len(b.Hosts) > 1 {
		return nil, fmt.Errorf("secret %s cannot set explicit route %q with multiple hosts", b.Name, b.Route)
	}
	out := make([]BoundRouteTarget, 0, len(b.Hosts))
	slugHost := make(map[string]string, len(b.Hosts))
	for _, h := range b.Hosts {
		slug := ""
		if builtInRoute, ok := agents.FindGatewayRouteForHost(h); ok && builtInRoute != "" {
			if b.Route != "" && b.Route != builtInRoute {
				return nil, fmt.Errorf("secret %s route %q conflicts with built-in route %q for %s", b.Name, b.Route, builtInRoute, h)
			}
			slug = builtInRoute
		} else if b.Route != "" {
			slug = b.Route
		} else {
			slug = api.RouteSlugForHost(h)
		}
		if slug == "" {
			return nil, fmt.Errorf("secret %s host %q has no valid gateway route slug", b.Name, h)
		}
		if prev, taken := slugHost[slug]; taken {
			if prev == h {
				continue
			}
			return nil, fmt.Errorf("secret %s hosts %s and %s collide on gateway route %q", b.Name, prev, h, slug)
		}
		slugHost[slug] = h
		out = append(out, BoundRouteTarget{Slug: slug, Host: h})
	}
	return out, nil
}

// resolveProcessSecrets classifies every secret requested for a process-wall
// session into bound secrets (held only by the egress gateway) or explicitly
// exposed secrets (--expose NAME). Any secret with no bound hosts that was not
// explicitly exposed fails closed with secret_unbound (isolation spec 7.3, 9.3),
// and any gateway route collision fails closed with bad_secret.
func resolveProcessSecrets(
	agent agents.Agent,
	known bool,
	secretNames []string,
	userBinds map[string]api.SecretBind,
	exposed []string,
) (bound []api.SecretBind, exposedOut []string, err error) {
	if len(secretNames) == 0 {
		return nil, nil, nil
	}
	expSet := make(map[string]bool, len(exposed))
	for _, n := range exposed {
		if trimmed := strings.TrimSpace(n); trimmed != "" {
			expSet[trimmed] = true
		}
	}

	for _, name := range secretNames {
		if expSet[name] {
			exposedOut = append(exposedOut, name)
			continue
		}
		if ub, ok := userBinds[name]; ok && len(ub.Hosts) > 0 {
			b, berr := normalizeSecretBind(name, ub, agent, known)
			if berr != nil {
				return nil, nil, berr
			}
			bound = append(bound, b)
			continue
		}
		if b, ok := agentSecretBind(name, agent, known); ok {
			bound = append(bound, b)
			continue
		}
		return nil, nil, UserError(
			"secret_unbound",
			name+" has no hosts, so it would enter the sandbox as a plain value",
			fmt.Sprintf("Add hosts = [\"api.example.com\"] to [secrets.%s] in config.toml, or pass --expose %s.", name, name),
		)
	}
	if err := checkBoundRouteCollisions(agent, bound); err != nil {
		return nil, nil, err
	}
	sort.Strings(exposedOut)
	return bound, exposedOut, nil
}

// checkBoundRouteCollisions rejects any route slug collision against built-in
// model gateway routes or between two bound secrets (or two hosts deriving the
// same slug) so a credential can never be armed onto another host's route.
func checkBoundRouteCollisions(agent agents.Agent, bound []api.SecretBind) error {
	routeHost := map[string]string{}
	// Seed every built-in model gateway route (both for this agent and across
	// all agents) so custom secrets can never hijack a built-in route slug.
	for _, rc := range append(egress.DefaultGatewayRoutes(agent.Kind), egress.DefaultGatewayRoutes("")...) {
		u, err := url.Parse(rc.Upstream)
		if err != nil || u.Hostname() == "" {
			continue
		}
		if _, exists := routeHost[rc.Route]; !exists {
			routeHost[rc.Route] = strings.ToLower(u.Hostname())
		}
	}

	routeOwner := map[string]string{}
	for _, b := range bound {
		targets, err := BoundSecretRoutes(b)
		if err != nil {
			return UserError("bad_secret", err.Error(), fmt.Sprintf("Pick a distinct route in [secrets.%s] in config.toml.", b.Name))
		}
		for _, t := range targets {
			if existingHost, taken := routeHost[t.Slug]; taken && existingHost != t.Host {
				return UserError(
					"bad_secret",
					fmt.Sprintf("secret %s route %q for %s collides with gateway route for %s", b.Name, t.Slug, t.Host, existingHost),
					fmt.Sprintf("Pick a distinct route in [secrets.%s] in config.toml.", b.Name),
				)
			}
			if owner, owned := routeOwner[t.Slug]; owned && owner != b.Name {
				return UserError(
					"bad_secret",
					fmt.Sprintf("secrets %s and %s both claim gateway route %q", owner, b.Name, t.Slug),
					fmt.Sprintf("Pick a distinct route in [secrets.%s] in config.toml, or pass only one of those secrets.", b.Name),
				)
			}
			routeHost[t.Slug] = t.Host
			routeOwner[t.Slug] = b.Name
		}
	}
	return nil
}

func normalizeSecretBind(name string, raw api.SecretBind, agent agents.Agent, known bool) (api.SecretBind, error) {
	hosts := make([]string, 0, len(raw.Hosts))
	for _, h := range raw.Hosts {
		norm, err := api.NormalizeSecretHost(h)
		if err != nil {
			return api.SecretBind{}, UserError("bad_secret", fmt.Sprintf("secret %s: %s", name, err.Error()), "Fix [secrets."+name+"] in config.toml.")
		}
		if !slices.Contains(hosts, norm) {
			hosts = append(hosts, norm)
		}
	}
	if len(hosts) == 0 {
		return api.SecretBind{}, UserError("secret_unbound",
			name+" has no hosts, so it would enter the sandbox as a plain value",
			fmt.Sprintf("Add hosts = [\"api.example.com\"] to [secrets.%s] in config.toml, or pass --expose %s.", name, name))
	}
	route, err := api.NormalizeGatewayRoute(raw.Route)
	if err != nil {
		return api.SecretBind{}, UserError("bad_secret", fmt.Sprintf("secret %s: %s", name, err.Error()), "Fix [secrets."+name+"] in config.toml.")
	}
	if route != "" && len(hosts) > 1 {
		return api.SecretBind{}, UserError(
			"bad_secret",
			fmt.Sprintf("secret %s cannot set explicit route %q with multiple hosts", name, route),
			fmt.Sprintf("Remove route or list only one host in [secrets.%s] in config.toml.", name),
		)
	}
	header := strings.TrimSpace(raw.Header)
	format := strings.TrimSpace(raw.Format)
	if header != "" && !api.ValidHeaderName(header) {
		return api.SecretBind{}, UserError("bad_secret", fmt.Sprintf("secret %s has invalid header %q", name, header), "Use a valid HTTP header name such as Authorization or x-api-key.")
	}
	if strings.ContainsAny(format, "\r\n") {
		return api.SecretBind{}, UserError("bad_secret", fmt.Sprintf("secret %s format cannot contain CR or LF characters", name), "Fix format in [secrets."+name+"] in config.toml.")
	}
	// Inherit default header/format from the agent table when the user only
	// specified hosts in [secrets.NAME].
	if ab, ok := lookupAgentSecret(name, agent, known); ok {
		if header == "" {
			header = ab.Header
		}
		if format == "" {
			format = ab.Format
		}
	}
	if header == "" {
		header = "Authorization"
	}
	if format == "" {
		if strings.EqualFold(header, "Authorization") {
			format = "Bearer {value}"
		} else {
			format = "{value}"
		}
	}
	return api.SecretBind{
		Name:   name,
		Hosts:  hosts,
		Header: header,
		Format: format,
		Route:  route,
	}, nil
}

func agentSecretBind(name string, agent agents.Agent, known bool) (api.SecretBind, bool) {
	ab, ok := lookupAgentSecret(name, agent, known)
	if !ok || len(ab.Hosts) == 0 {
		return api.SecretBind{}, false
	}
	hosts := make([]string, 0, len(ab.Hosts))
	for _, h := range ab.Hosts {
		if norm, err := api.NormalizeSecretHost(h); err == nil && !slices.Contains(hosts, norm) {
			hosts = append(hosts, norm)
		}
	}
	if len(hosts) == 0 {
		return api.SecretBind{}, false
	}
	route := ""
	if builtInRoute, ok := agents.FindGatewayRouteForHost(hosts[0]); ok && builtInRoute != "" {
		route = builtInRoute
	}
	return api.SecretBind{
		Name:   name,
		Hosts:  hosts,
		Header: ab.Header,
		Format: ab.Format,
		Route:  route,
	}, true
}

// detectSubscriptionAuth determines whether a walled session should arm the
// agent's SubscriptionGateway (such as Codex ChatGPT sign-in on chatgpt.com)
// rather than its API-key ModelGateway. A bound subscription secret (such as
// CODEX_AUTH_JSON) always selects subscription mode; otherwise explicit API-key
// credentials win over a host-side login file.
func detectSubscriptionAuth(agent agents.Agent, known bool, bound []api.SecretBind, exposed []string) string {
	if !known || agent.SubscriptionGateway == nil {
		return ""
	}
	sub := agent.SubscriptionGateway
	if sub.SecretEnv != "" {
		for _, b := range bound {
			if b.Name == sub.SecretEnv {
				return "secret:" + sub.SecretEnv
			}
		}
	}
	if agent.ModelGateway != nil {
		gwHost := agent.ModelGateway.UpstreamHost()
		for _, b := range bound {
			if slices.Contains(b.Hosts, gwHost) {
				return ""
			}
		}
		for _, binding := range agent.Secrets {
			if slices.Contains(binding.Hosts, gwHost) && slices.Contains(exposed, binding.Env) {
				return ""
			}
		}
	}
	if sub.AuthFile == "" || sub.AuthFormat != egress.AuthFormatCodexChatGPT {
		return ""
	}
	toks, err := egress.ReadCodexAuthFile(expandHome("~/" + sub.AuthFile))
	if err != nil {
		return ""
	}
	toks.Clear()
	return "host"
}

func lookupAgentSecret(name string, agent agents.Agent, known bool) (agents.SecretBinding, bool) {
	if known {
		if b, ok := agents.FindSecretBinding(agent, name); ok {
			return b, true
		}
	}
	_, b, ok := agents.FindSecretBindingAnywhere(name)
	return b, ok
}
