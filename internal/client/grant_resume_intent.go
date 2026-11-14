package client

import (
	"context"
	"fmt"
	"sort"
)

func creationGrantFacts(ctx context.Context) (string, []string) {
	intent := requestWallIntent(ctx)
	if intent == nil {
		return "none", nil
	}
	names := make([]string, 0, len(intent.exposed))
	for name := range intent.exposed {
		names = append(names, name)
	}
	sort.Strings(names)
	return intent.level, names
}

// allowResumeSecretNames derives authority from local creation facts only.
// Neither a host's weaker wall nor a new --expose answer can replace them.
func allowResumeSecretNames(ctx context.Context, h Host, hostID string, tier Tier, settings Settings, names []string, creation *SandboxPolicy) error {
	if creation != nil && creation.FutureLineage && len(names) != 0 {
		return futureLineageError()
	}
	if creation != nil && creation.lineage != nil {
		if err := checkLocalSandboxNames(creation, names); err != nil {
			return err
		}
	}
	var intent *wallGrantIntent
	if creation != nil {
		if !validCreationWall(creation.CreationWall) {
			return badSandboxState()
		}
		if requested := requestWallIntent(ctx); requested != nil && requested.level != creation.CreationWall {
			return &UserError{Msg: "the requested resume wall differs from this laptop's creation record",
				Hint: "Start a new session after resolving its wall and secret exposure.", Code: "wall_intent", Kind: KindRefused}
		}
		intent = &wallGrantIntent{level: creation.CreationWall, exposed: make(map[string]bool, len(creation.Exposed))}
		for _, name := range creation.Exposed {
			intent.exposed[name] = true
		}
	}
	tier = min(tier, requestCeiling(ctx, tier))
	return allowSecretNamesAtWall(ctx, h, hostID, tier, settings, names, intent, creation != nil)
}

func missingResumeIntent(name string) error {
	return &UserError{Msg: fmt.Sprintf("this laptop has no creation-wall authorisation to resend %s to that session", name),
		Hint: "Start a new session with the intended wall and secret exposure, or use a tier-scoped token. Another laptop's session record cannot authorise this send.",
		Code: "secret_intent_missing", Kind: KindRefused}
}
