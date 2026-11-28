package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/egress"
	"github.com/iamlakshya/simstim/internal/guest"
	"github.com/iamlakshya/simstim/internal/host"
	"github.com/iamlakshya/simstim/internal/proto"
)

// gatewayRoutes returns the full set of gateway routes for this session's
// egress process: the agent's built-in model gateway routes plus any custom
// destination routes required by bound secrets in the manifest. Conflicting
// route slugs never overwrite an existing route's upstream host.
func (s *Supervisor) gatewayRoutes() []egress.GatewayRouteConfig {
	routes := egress.DefaultGatewayRoutes(s.m.Agent.Kind)
	routeHosts := make(map[string]string, len(routes))
	for _, r := range routes {
		if u, err := url.Parse(r.Upstream); err == nil && u.Hostname() != "" {
			routeHosts[r.Route] = strings.ToLower(u.Hostname())
		}
	}
	if s.m.Wall == nil {
		return routes
	}
	for _, b := range s.m.Wall.BoundSecrets {
		targets, err := host.BoundSecretRoutes(b)
		if err != nil {
			continue
		}
		for _, target := range targets {
			if _, taken := routeHosts[target.Slug]; taken {
				continue
			}
			routeHosts[target.Slug] = target.Host
			routes = append(routes, egress.GatewayRouteConfig{
				Route:    target.Slug,
				Upstream: "https://" + target.Host,
			})
		}
	}
	return routes
}

func (s *Supervisor) gatewayRouteHostMap() map[string]string {
	routes := s.gatewayRoutes()
	out := make(map[string]string, len(routes))
	for _, r := range routes {
		if u, err := url.Parse(r.Upstream); err == nil && u.Hostname() != "" {
			out[r.Route] = strings.ToLower(u.Hostname())
		}
	}
	return out
}

func (s *Supervisor) boundSecretMap() map[string]api.SecretBind {
	if s.m.Wall == nil || len(s.m.Wall.BoundSecrets) == 0 {
		return nil
	}
	out := make(map[string]api.SecretBind, len(s.m.Wall.BoundSecrets))
	for _, b := range s.m.Wall.BoundSecrets {
		out[b.Name] = b
	}
	return out
}

// buildBoundCredentials validates every bound secret against the registered
// route -> upstreamHost map so no secret can ever be armed onto a route whose
// upstream host is outside that secret's Hosts list or onto a route already
// claimed by another bound secret.
func (s *Supervisor) buildBoundCredentials(
	boundMap map[string]api.SecretBind,
	names []string,
	values map[string]api.Secret,
) (creds []egress.BoundCredential, armedNames []string, exposedEnv []string, err error) {
	registered := s.gatewayRouteHostMap()
	claimedBy := map[string]string{}
	for _, name := range names {
		val, ok := values[name]
		if !ok {
			continue
		}
		b, isBound := boundMap[name]
		if !isBound {
			exposedEnv = append(exposedEnv, name+"="+string(val))
			continue
		}
		targets, rerr := host.BoundSecretRoutes(b)
		if rerr != nil {
			return nil, nil, nil, fmt.Errorf("bound secret %s: %w", b.Name, rerr)
		}
		for _, target := range targets {
			upHost, exists := registered[target.Slug]
			if !exists || upHost != target.Host || !slices.Contains(b.Hosts, upHost) {
				return nil, nil, nil, fmt.Errorf("bound secret %s route %q points to %q, not its bound host %q", b.Name, target.Slug, upHost, target.Host)
			}
			if prev, dup := claimedBy[target.Slug]; dup && prev != b.Name {
				return nil, nil, nil, fmt.Errorf("bound secrets %s and %s both claim route %q", prev, b.Name, target.Slug)
			}
			claimedBy[target.Slug] = b.Name
			creds = append(creds, egress.BoundCredential{
				Route:  target.Slug,
				Name:   b.Name,
				Header: b.Header,
				Format: b.Format,
				Value:  val,
			})
		}
		armedNames = append(armedNames, name)
	}
	return creds, armedNames, exposedEnv, nil
}

// splitAndArmSecrets separates bound secrets (sent only to the egress process
// in memory) from explicitly exposed secrets (passed to the guest holder's env).
// Every bound secret value is cleared from supervisor memory before returning.
func (s *Supervisor) splitAndArmSecrets(want []string, received map[string]api.Secret) (exposedEnv []string, err error) {
	defer clear(received)
	creds, _, exposedEnv, err := s.buildBoundCredentials(s.boundSecretMap(), want, received)
	defer func() {
		for i := range creds {
			creds[i].Value = ""
		}
		clear(creds)
	}()
	if err != nil {
		clear(exposedEnv)
		return nil, err
	}

	if len(creds) == 0 {
		return exposedEnv, nil
	}
	if s.egress == nil {
		clear(exposedEnv)
		return nil, errors.New("egress process is not running; bound secrets cannot be armed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.egress.ArmSecrets(ctx, creds); err != nil {
		clear(exposedEnv)
		return nil, fmt.Errorf("arm bound secrets in egress: %w", err)
	}
	s.audit.Record(AuditEntry{
		Event:   "secrets_armed",
		Session: s.id,
		Detail:  fmt.Sprintf("routes=%d", len(creds)),
	})
	return exposedEnv, nil
}

// agentBoundSecretEnv returns the bound secret name that supplies credentials
// for this session's agent ModelGateway (such as ANTHROPIC_API_KEY,
// CLAUDE_CODE_OAUTH_TOKEN or OPENAI_API_KEY), or "" to use the agent's DummyEnv.
func (s *Supervisor) agentBoundSecretEnv() string {
	if s.m.Wall == nil || len(s.m.Wall.BoundSecrets) == 0 {
		return ""
	}
	agent, ok := agents.Find(s.m.Agent.Kind)
	if !ok || agent.ModelGateway == nil {
		return ""
	}
	gwHost := agent.ModelGateway.UpstreamHost()
	for _, b := range s.m.Wall.BoundSecrets {
		if _, matched := agents.FindSecretBinding(agent, b.Name); matched {
			return b.Name
		}
	}
	for _, b := range s.m.Wall.BoundSecrets {
		if slices.Contains(b.Hosts, gwHost) {
			return b.Name
		}
	}
	return ""
}

// boundPlaceholders returns non-secret NAME=placeholder pairs for bound secrets
// that are not already covered by the agent's own ModelGateway env.
func (s *Supervisor) boundPlaceholders(agentEnv string) []string {
	if s.m.Wall == nil || len(s.m.Wall.BoundSecrets) == 0 {
		return nil
	}
	var out []string
	for _, b := range s.m.Wall.BoundSecrets {
		if b.Name == "" || b.Name == agentEnv {
			continue
		}
		out = append(out, b.Name+"="+guest.GatewayPlaceholder(b.Name))
	}
	sort.Strings(out)
	return out
}

// handleArm installs bound credentials into the running egress process after a
// restart (or re-arm) without touching disk or entering the sandbox.
func (s *Supervisor) handleArm(w *proto.Writer, req proto.Request) {
	reply := func(v any, perr *proto.Error) {
		resp := proto.Response{ID: req.ID, OK: perr == nil, Error: perr}
		if v != nil {
			resp.Result, _ = json.Marshal(v)
		}
		_ = w.WriteJSON(proto.TResponse, resp)
	}
	if s.egress == nil {
		reply(nil, &proto.Error{Code: "unsupported", Message: "this session has no egress gateway to arm", Hint: "Start the session with --wall process."})
		return
	}
	var msg host.SecretsPayload
	if err := json.Unmarshal(req.Args, &msg); err != nil {
		reply(nil, &proto.Error{Code: "bad_request", Message: "the secrets payload is not valid JSON", Hint: "Update the client."})
		return
	}
	defer clear(msg.Env)

	boundMap := s.boundSecretMap()
	names := make([]string, 0, len(msg.Env))
	for k := range msg.Env {
		if _, ok := boundMap[k]; ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)

	creds, armed, _, err := s.buildBoundCredentials(boundMap, names, msg.Env)
	defer func() {
		for i := range creds {
			creds[i].Value = ""
		}
		clear(creds)
	}()
	if err != nil {
		reply(nil, &proto.Error{Code: "bad_secret", Message: err.Error(), Hint: "Fix colliding bound secret routes in config.toml."})
		return
	}
	if len(creds) == 0 {
		reply(map[string]any{"armed": []string{}}, nil)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.egress.ArmSecrets(ctx, creds); err != nil {
		reply(nil, &proto.Error{Code: "arm_failed", Message: err.Error(), Hint: "Retry arming the session."})
		return
	}
	s.mu.Lock()
	if len(s.m.MissingSecrets) > 0 {
		armedSet := make(map[string]bool, len(armed))
		for _, n := range armed {
			armedSet[n] = true
		}
		kept := s.m.MissingSecrets[:0]
		for _, n := range s.m.MissingSecrets {
			if !armedSet[n] {
				kept = append(kept, n)
			}
		}
		if len(kept) == 0 {
			s.m.MissingSecrets = nil
		} else {
			s.m.MissingSecrets = slices.Clone(kept)
		}
	}
	mCopy := *s.m
	s.mu.Unlock()
	_ = s.save(&mCopy, false)
	s.audit.Record(AuditEntry{Event: "secrets_armed", Session: s.id, Detail: fmt.Sprintf("routes=%d", len(creds))})
	reply(map[string]any{"armed": armed}, nil)
}
