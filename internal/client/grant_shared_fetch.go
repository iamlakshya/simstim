package client

import (
	"context"
	"slices"
	"sort"
)

// The command fetcher also honours a prepared shared request. A mutation
// after the first precheck must stop values before the first command.
func checkSharedSecretFetch(ctx context.Context, h Host, id string, names []string) error {
	r := grantRequestOf(ctx)
	if r == nil {
		return nil
	}
	r.mu.Lock()
	join := cloneJoinGrantIntent(r.join)
	resume := cloneResumeGrantIntent(r.resume)
	groupRequired := r.resumeGroup
	r.mu.Unlock()
	if groupRequired && resume == nil {
		return grantOperationError()
	}
	canonical := slices.Clone(names)
	sort.Strings(canonical)
	if join != nil {
		if join.connection == nil || !SameEntry(join.connection.Host, h) || id != join.parent {
			return joinIntentError()
		}
		_, _, err := join.connection.joinGrantIntent(ctx, id, canonical, true)
		return err
	}
	if resume != nil {
		member, ok := resume.members[id]
		if resume.connection == nil || !SameEntry(resume.connection.Host, h) || !ok ||
			!slices.Equal(member.names, canonical) {
			return joinIntentError()
		}
		_, _, err := resume.connection.prepareResumeMembers(ctx, resume.original, resume.originalNames)
		return err
	}
	return requireGrantOperation(ctx, grantOpResume)
}

func checkDerivedSecretFetch(ctx context.Context, p *SandboxPolicy) error {
	if p == nil || p.lineage == nil && !p.FutureLineage {
		return nil
	}
	r := grantRequestOf(ctx)
	if r == nil {
		return joinIntentError()
	}
	r.mu.Lock()
	prepared := r.join != nil || r.resume != nil
	r.mu.Unlock()
	if !prepared {
		return &UserError{Msg: "this member needs a complete shared-session grant check before reading values",
			Hint: "Resolve its original session through the shared Join or Resume command.", Code: "join_intent", Kind: KindRefused}
	}
	return nil
}
