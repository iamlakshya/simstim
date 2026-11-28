package client

import (
	"fmt"
	"slices"
	"strings"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

// SecretConfig is one parsed entry under [secrets] in config.toml. It supports
// both the plain string form (`NAME = "command"`) and the table form
// (`[secrets.NAME]` with `run`, `hosts`, `header`, `format`, `route`, `on_hosts`,
// `kind`).
type SecretConfig struct {
	Name    string
	Run     string
	Hosts   []string
	Header  string
	Format  string
	Route   string
	OnHosts []string
	Kind    string
}

// ParseSecretConfig decodes raw from Settings.Secrets[name].
func ParseSecretConfig(name string, raw any) (SecretConfig, error) {
	cfg := SecretConfig{Name: name}
	switch v := raw.(type) {
	case string:
		cfg.Run = strings.TrimSpace(v)
		return cfg, nil
	case map[string]any:
		return parseSecretTable(name, v)
	default:
		return SecretConfig{}, fmt.Errorf("secret %q in [secrets] must be a command string or a [secrets.%s] table", name, name)
	}
}

func parseSecretTable(name string, tbl map[string]any) (SecretConfig, error) {
	cfg := SecretConfig{Name: name}
	var runCmd, aliasCmd string
	for k, val := range tbl {
		var err error
		switch k {
		case "run":
			runCmd, err = stringField(name, k, val)
		case "command":
			aliasCmd, err = stringField(name, k, val)
		case "hosts":
			cfg.Hosts, err = parseHostList(name, val)
		case "header":
			cfg.Header, err = parseHeaderField(name, val)
		case "format":
			cfg.Format, err = parseFormatField(name, val)
		case "route":
			var rawRoute string
			if rawRoute, err = stringField(name, k, val); err == nil {
				cfg.Route, err = api.NormalizeGatewayRoute(rawRoute)
			}
		case "on_hosts":
			cfg.OnHosts, err = stringListField(name, k, val)
		case "kind":
			cfg.Kind, err = stringField(name, k, val)
		default:
			return SecretConfig{}, fmt.Errorf("unknown key %q in [secrets.%s]", k, name)
		}
		if err != nil {
			return SecretConfig{}, err
		}
	}
	if runCmd != "" && aliasCmd != "" {
		return SecretConfig{}, fmt.Errorf("[secrets.%s] sets both run and command; keep run", name)
	}
	if cfg.Route != "" && len(cfg.Hosts) > 1 {
		return SecretConfig{}, fmt.Errorf("[secrets.%s] cannot set route %q with multiple hosts; remove route or list one host", name, cfg.Route)
	}
	cfg.Run = strings.TrimSpace(runCmd)
	if cfg.Run == "" {
		cfg.Run = strings.TrimSpace(aliasCmd)
	}
	return cfg, nil
}

func stringField(secret, key string, val any) (string, error) {
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("[secrets.%s] %s must be a string", secret, key)
	}
	return strings.TrimSpace(s), nil
}

func stringListField(secret, key string, val any) ([]string, error) {
	items, ok := val.([]any)
	if !ok {
		if strs, ok2 := val.([]string); ok2 {
			return slices.Clone(strs), nil
		}
		return nil, fmt.Errorf("[secrets.%s] %s must be a list of strings", secret, key)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("[secrets.%s] %s entries must be strings", secret, key)
		}
		out = append(out, strings.TrimSpace(s))
	}
	return out, nil
}

func parseHostList(secret string, val any) ([]string, error) {
	raw, err := stringListField(secret, "hosts", val)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raw))
	for _, h := range raw {
		norm, err := api.NormalizeSecretHost(h)
		if err != nil {
			return nil, fmt.Errorf("[secrets.%s] hosts: %w", secret, err)
		}
		if !slices.Contains(out, norm) {
			out = append(out, norm)
		}
	}
	return out, nil
}

func parseFormatField(secret string, val any) (string, error) {
	f, err := stringField(secret, "format", val)
	if err != nil {
		return "", err
	}
	if strings.ContainsAny(f, "\r\n") {
		return "", fmt.Errorf("[secrets.%s] format cannot contain CR or LF characters", secret)
	}
	return f, nil
}

func parseHeaderField(secret string, val any) (string, error) {
	h, err := stringField(secret, "header", val)
	if err != nil {
		return "", err
	}
	if h != "" && !api.ValidHeaderName(h) {
		return "", fmt.Errorf("[secrets.%s] header %q is not a valid HTTP header name", secret, h)
	}
	return h, nil
}

// SecretConfig returns the parsed secret definition for name, if present in s.
func (s Settings) SecretConfig(name string) (SecretConfig, bool, error) {
	raw, ok := s.Secrets[name]
	if !ok {
		return SecretConfig{}, false, nil
	}
	cfg, err := ParseSecretConfig(name, raw)
	if err != nil {
		return SecretConfig{}, true, err
	}
	return cfg, true, nil
}

// SecretBindsFor builds the non-secret destination binding metadata map for
// names from [secrets.NAME] tables in s. Never includes secret values.
func SecretBindsFor(s Settings, names []string) map[string]api.SecretBind {
	var out map[string]api.SecretBind
	for _, name := range names {
		cfg, ok, err := s.SecretConfig(name)
		if !ok || err != nil || len(cfg.Hosts) == 0 {
			continue
		}
		if out == nil {
			out = map[string]api.SecretBind{}
		}
		out[name] = api.SecretBind{
			Name:   name,
			Hosts:  slices.Clone(cfg.Hosts),
			Header: cfg.Header,
			Format: cfg.Format,
			Route:  cfg.Route,
		}
	}
	return out
}

// GatewaySecretNames returns the ordered, deduplicated secret names to fetch
// for a new session: the host's secrets, any --secret and --expose names, plus
// (for walled sessions whose agent declares a ModelGateway) the first matching
// secret from agent.Secrets present in s.Secrets when none of agent.Secrets was
// already named.
func GatewaySecretNames(s Settings, h Host, agentKind, wallLevel string, asked, exposed []string) []string {
	combined := make([]string, 0, len(asked)+len(exposed))
	combined = append(combined, asked...)
	combined = append(combined, exposed...)
	names := SecretNames(h, combined)

	// Only auto-select agent gateway secrets when wallLevel actually requests
	// a walled session (matching host.New: wallLevel != "" && wallLevel != "none").
	// When wallLevel is empty, host.New starts an unwalled session even if
	// h.WallFloor is set, so we must not silently inject API keys into it.
	if wallLevel == "" || wallLevel == "none" {
		return names
	}
	agent, ok := agents.Find(agentKind)
	if !ok || agent.ModelGateway == nil || len(agent.Secrets) == 0 {
		return names
	}
	for _, binding := range agent.Secrets {
		if slices.Contains(names, binding.Env) {
			return names
		}
	}
	for _, binding := range agent.Secrets {
		cfg, exists, err := s.SecretConfig(binding.Env)
		if exists && err == nil && cfg.Run != "" {
			return append(names, binding.Env)
		}
	}
	return names
}
