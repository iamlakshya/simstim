package client

import (
	"context"
	"slices"

	"github.com/iamlakshya/simstim/internal/api"
)

// The classifier's protected role is distinct from primary guest delivery.
// A provider machine still needs the actual original explicit exposure before
// either role can run its value command. Names and endpoint metadata grant none.
func (p *trustedJudgePlanV1) checkMachineExposureV1(ctx context.Context, settings Settings) error {
	if p == nil || p.conn == nil {
		return trustedJudgeSourceErrorV1()
	}
	if !IsSessionMachineV1(p.conn.Host) || p.name == "" {
		return nil
	}
	intent := requestWallIntent(ctx)
	if p.family == grantOpNew {
		if p.newPlan == nil || p.newPlan.selection == nil || p.newPlan.intent == nil || !slices.Contains(p.newPlan.selection.names, p.name) || p.newPlan.intent.Modes[p.name] != api.SecretModeExposed {
			return checkSessionMachineSecretV1(p.conn.Host, settings, p.name, nil)
		}
	} else {
		creation := p.creation
		if creation == nil || creation.FutureDelivery || creation.FutureLineage || !validCreationWall(creation.CreationWall) {
			return trustedJudgeSourceErrorV1()
		}
		intent = &wallGrantIntent{level: creation.CreationWall, exposed: map[string]bool{}}
		for _, name := range creation.Exposed {
			intent.exposed[name] = true
		}
	}
	return checkSessionMachineSecretV1(p.conn.Host, settings, p.name, intent)
}

// This is observed ledger metadata, never a grant or a source of consent.
// Named keys remain raw host-only signer values, outside the guest environment.
func sessionMachineGrantExposureV1(ctx context.Context, h Host, kind OutboundKind) string {
	r := grantRequestOf(ctx)
	if r != nil && requestGrantPurpose(r) == grantRemote && IsSessionMachineV1(h) && (kind == OutboundSecret || kind == OutboundSSHKey) {
		return "host"
	}
	return ""
}

func mergeGrantExposureV1(a, b string) string {
	if a == "host" || b == "host" {
		return "host"
	}
	if a != "" {
		return a
	}
	return b
}

func compatibleGrantExposureV1(recorded, next string) bool {
	return next == "" || recorded == "" || recorded == "host"
}
