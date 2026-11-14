package client

import (
	"context"
	"fmt"
)

// wallGrantIntent is immutable after construction. Host RPCs never carry
// it, and a host's secret mode cannot manufacture an exposure answer.
type wallGrantIntent struct {
	level   string
	exposed map[string]bool
}

// NewWallGrantRequest prepares one resolved start's local wall and exposure
// intent. Resolve and confirm the actual wall before calling this function
// or fetching values. Reuse the returned pointer only for this start's retries.
func NewWallGrantRequest(untrusted, sshAgent bool, resolvedWall string, exposeNames []string) (*GrantRequest, error) {
	switch resolvedWall {
	case "none", "process", "gvisor", "microvm":
	default:
		return nil, &UserError{Msg: "a wall grant request needs its resolved level",
			Hint: "Resolve auto and confirm any downgrade before fetching secrets.", Code: "bad_wall", Kind: KindUsage}
	}
	if sshAgent && resolvedWall != "none" {
		return nil, agentWallIntentError()
	}
	intent := &wallGrantIntent{level: resolvedWall, exposed: make(map[string]bool, len(exposeNames))}
	for _, name := range exposeNames {
		if !secretNameRE.MatchString(name) || name == "SSH_AUTH_SOCK" {
			return nil, &UserError{Msg: "an exposed secret name is invalid or reserved",
				Hint: "Name a configured secret with letters, digits and underscores.", Code: "bad_secret", Kind: KindUsage}
		}
		intent.exposed[name] = true
	}
	r := NewGrantRequest(untrusted, sshAgent)
	r.wall = intent
	return r, nil
}

func checkWallGrantArgs(ctx context.Context, actual string, sshAgent bool) error {
	if actual == "" {
		actual = "none"
	}
	r := grantRequestOf(ctx)
	if r != nil {
		sshAgent = sshAgent || r.SSHAgent
	}
	if sshAgent && actual != "none" {
		return agentWallIntentError()
	}
	if err := checkWallAgentIntent(ctx, sshAgent); err != nil {
		return err
	}
	if r == nil || r.wall == nil {
		if actual == "none" {
			return nil
		}
	} else if actual == r.wall.level {
		return nil
	}
	return &UserError{Msg: "the outgoing wall differs from this start's local grant intent",
		Hint: "Resolve the wall again and confirm its grants before fetching secrets.", Code: "wall_intent", Kind: KindRefused}
}

// checkRequestedGrantWall keeps the current early wall safety interlock
// ahead of value fetching. The wall launcher owns replacing that interlock.
func checkRequestedGrantWall(ctx context.Context) error {
	if r := grantRequestOf(ctx); r != nil && r.wall != nil {
		return CheckRequestedWall(r.wall.level)
	}
	return nil
}

func agentWallIntentError() error {
	return &UserError{Msg: "a sandbox cannot receive this computer's full SSH agent",
		Hint: "Use a separate deploy key, or explicitly choose an unwalled session on a host you own.", Code: "ssh_agent_refused", Kind: KindRefused}
}

func checkWallAgentIntent(ctx context.Context, sshAgent bool) error {
	if r := grantRequestOf(ctx); r != nil && r.wall != nil && r.wall.level != "none" && (sshAgent || r.SSHAgent) {
		return agentWallIntentError()
	}
	return nil
}

func requestWallIntent(ctx context.Context) *wallGrantIntent {
	if r := grantRequestOf(ctx); r != nil {
		return r.wall
	}
	return nil
}

func minimumSecretTier(settings Settings, name string) (Tier, error) {
	if raw, ok := settings.SecretTiers[name]; ok {
		return ParseTier(raw)
	}
	return TierOwn, nil
}

func allowWallSecretIntent(ctx context.Context, settings Settings, name string) error {
	minimum, err := minimumSecretTier(settings, name)
	if err != nil {
		return err
	}
	return allowSecretAtWall(requestWallIntent(ctx), name, minimum)
}

func allowSecretAtWall(intent *wallGrantIntent, name string, minimum Tier) error {
	if intent == nil || intent.level != "process" || minimum != TierOwn || intent.exposed[name] {
		return nil
	}
	return &UserError{Msg: fmt.Sprintf("%s needs an explicit exposure answer before entering a process wall", name),
		Hint: "Start a new process session with --expose " + name + " only if it may read the value.", Code: "secret_expose", Kind: KindRefused}
}
