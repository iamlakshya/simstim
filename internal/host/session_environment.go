package host

import (
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
)

func environmentMap(entries []string) map[string]string {
	result := make(map[string]string, len(entries))
	for _, entry := range entries {
		if name, value, ok := strings.Cut(entry, "="); ok {
			result[name] = value
		}
	}
	return result
}

func orderedEnvironment(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for name := range values {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, name := range keys {
		result = append(result, name+"="+values[name])
	}
	return result
}

func applyResolvedEnvironment(base, controls map[string]string, environment *api.SessionEnvironment, state *api.SessionSecretState, names []string) ([]string, error) {
	values, err := api.ApplySessionEnvironment(base, environment, state, names)
	if err != nil {
		var invalid *api.SessionEnvironmentError
		if errors.As(err, &invalid) {
			return nil, UserError(invalid.Code, invalid.Message, invalid.Hint)
		}
		return nil, err
	}
	// These come from the same current runtime constructor, never an old
	// merged Manifest.Env. Missing optional controls remain absent.
	for name, value := range controls {
		if value == "" {
			delete(values, name)
		} else {
			values[name] = value
		}
	}
	return orderedEnvironment(values), nil
}

func resolvedWalledEnv(a NewArgs, id string, dirs []string, agent agents.Agent, inline bool, state *api.SessionSecretState, names []string) ([]string, string, error) {
	if a.Environment == nil {
		env, term := walledEnv(a, id, dirs, agent, inline)
		return env, term, nil // Existing clientless/legacy path, never a lookup directive.
	}
	base, term := walledEnvironmentBase(a, id, dirs, agent, inline)
	env, err := applyResolvedEnvironment(base, base, a.Environment, state, names)
	return env, term, err
}

func resolvedUnwalledEnv(a NewArgs, id string, login []string, agent agents.Agent, inline bool, state *api.SessionSecretState, names []string) ([]string, string, error) {
	base, term := sessionEnv(a, id, login, agent, inline)
	if a.Environment == nil {
		return base, term, nil
	}
	controls, _ := sessionVars(a, id, "", agent, inline)
	// The ordinary holder's existing hook selector is already in base.
	for name := range controls {
		if value, ok := environmentMap(base)[name]; ok {
			controls[name] = value
		}
	}
	env, err := applyResolvedEnvironment(environmentMap(base), controls, a.Environment, state, names)
	return env, term, err
}

// prepareResumeEnvironment rebuilds the actual current runtime base before
// applying a supported complete map. It never reads old Manifest.Env as base
// or looks up a laptop directive in the host environment.
func prepareResumeEnvironment(m *Manifest, environment *api.SessionEnvironment) error {
	if environment == nil {
		return nil
	} // Preserve genuine unknown legacy semantics.
	argv, _, err := resumeArgv(m)
	if err != nil {
		return err
	}
	if len(argv) == 0 {
		return UserError("bad_env", "the resume command has no executable", "Select the original supported command before resuming.")
	}
	login, err := LoginEnv(15 * time.Second)
	if err != nil {
		return UserError("bad_env", "the current runtime environment cannot be prepared", "Check the host login environment before resuming.")
	}
	agent, _ := agents.Find(m.Agent.Kind)
	inline := len(m.Agent.Inline) > 0 && agent.Inline.Has()
	args := NewArgs{Term: m.Term.Term, Environment: environment}
	var env []string
	var term string
	if m.Wall != nil {
		bin := argv[0]
		if !filepath.IsAbs(bin) {
			bin, err = lookPath(bin, envValue(login, "PATH"))
			if err != nil {
				return UserError("agent_missing", "the original runtime executable is unavailable", "Install the selected agent before resuming.")
			}
		}
		bin, err = filepath.EvalSymlinks(bin)
		if err != nil {
			return UserError("agent_missing", "the original runtime executable cannot be resolved", "Reinstall the selected agent before resuming.")
		}
		env, term, err = resolvedWalledEnv(args, m.ID, agentDirs(bin, envValue(login, "PATH")), agent, inline, m.SecretState, m.Secrets)
	} else {
		env, term, err = resolvedUnwalledEnv(args, m.ID, login, agent, inline, m.SecretState, m.Secrets)
		if err == nil {
			env, err = withSSHAgentSocket(env, m.ID, m.SSHAgent)
		}
	}
	if err != nil {
		return err
	}
	m.Env, m.Term.Term = slices.Clone(env), term
	applySessionIdentity(m)
	// Actual guest preparation applies its newly owned CA/proxy controls later.
	return nil
}
