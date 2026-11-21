package api

import (
	"fmt"
	"strings"
)

// Features required from the running supervisor, as well as its bridge.
const (
	FeatureExecStream = "exec-stream-v1"
	FeatureSpawn      = "spawn-v1"
)

// ExecArgs runs a command in an existing sandbox's workspace as the agent.
// It carries no host environment, working directory or credential choice.
type ExecArgs struct {
	ID        string   `json:"id"`
	Argv      []string `json:"argv"`
	TimeoutMS int64    `json:"timeout_ms,omitempty"`
}

// Normalise supplies the documented timeout and rejects invalid commands.
func (a *ExecArgs) Normalise() error {
	if !ValidSessionID(a.ID) {
		return fmt.Errorf("exec names no valid session. Try: list sessions and use their full address")
	}
	if len(a.Argv) == 0 || a.Argv[0] == "" {
		return fmt.Errorf("exec needs a command. Try: use exec <address> -- <command> [args]")
	}
	for _, arg := range a.Argv {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("exec arguments contain a NUL. Try: pass plain command arguments")
		}
	}
	if a.TimeoutMS == 0 {
		a.TimeoutMS = 600000
	}
	if a.TimeoutMS < 0 || a.TimeoutMS > 9223372036854 {
		return fmt.Errorf("exec timeout is outside the duration range. Try: use a positive timeout")
	}
	a.Argv = append([]string(nil), a.Argv...)
	return nil
}

// JoinArgs creates another terminal in an existing sandbox. Its environment,
// wall, workspace, files and grant ceiling come from the primary session.
type JoinArgs struct {
	// ProtectedTrustedSecretValues is private trusted-caller delivery, never guest environment.
	ProtectedTrustedSecretValues map[string]Secret `json:"protected_trusted_secret_values,omitempty"`
	// SecretGeneration restricts the sealed current cohort; it grants no local name authority.
	SecretGeneration   string                `json:"secret_generation,omitempty"`
	Snapshot           *SandboxSnapshot      `json:"snapshot"`
	Parent             string                `json:"parent"`
	ID                 string                `json:"id,omitempty"`
	Name               string                `json:"name,omitempty"`
	Agent              string                `json:"agent,omitempty"`
	Argv               []string              `json:"argv,omitempty"`
	Task               string                `json:"task,omitempty"`
	Cols               int                   `json:"cols,omitempty"`
	Rows               int                   `json:"rows,omitempty"`
	Term               string                `json:"term,omitempty"`
	ColorTerm          string                `json:"colorterm,omitempty"`
	TermProgram        string                `json:"term_program,omitempty"`
	TermProgramVersion string                `json:"term_program_version,omitempty"`
	Secrets            map[string]Secret     `json:"secrets,omitempty"`
	SecretDelivery     *SecretDeliveryIntent `json:"secret_delivery,omitempty"`
}

// ShellArgs chooses the newest live shell, or creates another shell terminal.
type ShellArgs struct {
	ID   string `json:"id"`
	New  bool   `json:"new,omitempty"`
	Name string `json:"name,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
	Term string `json:"term,omitempty"`
}
