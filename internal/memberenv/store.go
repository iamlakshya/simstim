// Package memberenv projects the same trusted member context for Store planning
// and actual guest execution. It performs no I/O and grants no authority.
package memberenv

import (
	"github.com/iamlakshya/simstim/internal/agents"
	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/proto"
	"path/filepath"
	"slices"
	"strings"
)

// StorePlan is one canonical command and public bound sentinel, never a value.
type StorePlan struct {
	Name     string
	Argv     []string
	Sentinel api.Secret
}

// PrepareStores resolves canonical Store commands and their complete filtered environment.
func PrepareStores(sessionID, agentKind, cwd string, baseEnv []string, sentinels map[string]api.Secret) ([]StorePlan, []string, error) {
	if !api.ValidSessionID(sessionID) {
		return nil, nil, StoreError("")
	}
	agent, known := agents.Find(agentKind)
	if !known {
		return nil, nil, nil
	}
	plans, credentials, err := PlanStores(agent.SecretBindings(), sentinels)
	if err != nil || len(plans) == 0 {
		return nil, nil, err
	}
	name := plans[0].Name
	if !filepath.IsAbs(cwd) || strings.ContainsRune(cwd, 0) {
		return nil, nil, StoreError(name)
	}
	env, err := StoreEnvironment(baseEnv, sentinels, credentials, name)
	if err != nil {
		return nil, nil, err
	}
	return plans, env, nil
}

// PlanStores selects only actual bound sentinels for canonical agent commands.
func PlanStores(rows []agents.SecretBinding, sentinels map[string]api.Secret) ([]StorePlan, map[string]bool, error) {
	credentials := make(map[string]bool, len(rows))
	var plans []StorePlan
	for _, row := range rows {
		if !EnvName(row.Env) || credentials[row.Env] {
			return nil, nil, StoreError("")
		}
		credentials[row.Env] = true
		if len(row.Store) == 0 {
			continue
		}
		if row.Store[0] == "" {
			return nil, nil, StoreError(row.Env)
		}
		for _, argument := range row.Store {
			if strings.ContainsRune(argument, 0) {
				return nil, nil, StoreError(row.Env)
			}
		}
		if sentinel, bound := sentinels[row.Env]; bound {
			plans = append(plans, StorePlan{Name: row.Env, Argv: slices.Clone(row.Store), Sentinel: sentinel})
		}
	}
	return plans, credentials, nil
}

// StoreEnvironment validates every base entry before dropping transient metadata.
func StoreEnvironment(baseEnv []string, sentinels map[string]api.Secret, credentials map[string]bool, name string) ([]string, error) {
	for variable, sentinel := range sentinels {
		if !EnvName(variable) || sentinel == "" || strings.ContainsRune(string(sentinel), 0) {
			return nil, StoreError(name)
		}
	}
	seen := make(map[string]bool, len(baseEnv))
	env := make([]string, 0, len(baseEnv))
	for _, entry := range baseEnv {
		variable, _, split := strings.Cut(entry, "=")
		_, bound := sentinels[variable]
		if !split || !EnvName(variable) || seen[variable] || bound || credentials[variable] || strings.ContainsRune(entry, 0) {
			return nil, StoreError(name)
		}
		seen[variable] = true
		// Validate before filtering. A dropped metadata name cannot hide a
		// duplicate/malformed or secret-bearing environment entry.
		if !StoreMetadata(variable) {
			env = append(env, entry)
		}
	}
	return env, nil
}

// StoreMetadata identifies transient session and terminal context.
// Store is a noninteractive stdin operation. These exact fields belong to
// session/terminal identity and must not select a different stored-login target.
// Every other caller variable, including all state/CA/proxy paths, is retained.
func StoreMetadata(name string) bool {
	if name == brand.Env("SESSION") || name == brand.Env("HOOK_SOCK") {
		return true
	}
	switch name {
	case "TERM", "COLORTERM", "TERM_PROGRAM", "TERM_PROGRAM_VERSION":
		return true
	}
	return false
}

// EnvName accepts only a credential environment variable name.
func EnvName(name string) bool {
	if name == "" {
		return false
	}
	for index := range len(name) {
		letter := name[index]
		if letter >= 'a' && letter <= 'z' || letter >= 'A' && letter <= 'Z' || letter == '_' || (index > 0 && letter >= '0' && letter <= '9') {
			continue
		}
		return false
	}
	return true
}

// StoreError describes failure without exposing any supplied material.
func StoreError(name string) error {
	message := "a bound secret could not be stored for this member"
	hint := "Check the agent's login setup and try again. Explicit exposure is only an alternative for a tool that reads its credential environment variable. A stored login may still be required."
	if EnvName(name) {
		message = "bound secret " + name + " could not be stored for this member"
		hint = "Check the agent's login setup and try again. Use --expose " + name + " only if the tool reads that variable. A stored login may still be required."
	}
	return &proto.Error{Code: "secret_store", Message: message, Hint: hint}
}
