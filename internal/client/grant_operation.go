package client

import (
	"context"
	"slices"
	"sort"
)

// This names value-selection families, not RPCs. Attach, reconnect, repo
// and paste continuations do not select a different family.
type grantOperation uint8

const (
	grantOpUnset grantOperation = iota
	grantOpNew
	grantOpJoin
	grantOpResume
	grantOpPush
	grantOpRootStep
)

type newGrantIntent struct {
	host     Host
	hostID   string
	names    []string
	sshAgent bool
	wall     string
	exposed  []string
}

type legacyResumeGrantIntent struct {
	host     Host
	hostID   string
	id       string
	names    []string
	creation *SandboxPolicy
}

func requireGrantOperation(ctx context.Context, op grantOperation) error {
	if err := requireRemoteGrantRequest(ctx); err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	if r == nil || op < grantOpNew || op > grantOpResume {
		return grantOperationError()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.lifetimeErrorLocked(); err != nil {
		return err
	}
	if r.operation != grantOpUnset && r.operation != op {
		return grantOperationError()
	}
	if r.operation != grantOpUnset && r.operationUntrusted != r.Untrusted {
		return grantOperationError()
	}
	r.operationUntrusted = r.Untrusted
	r.operation = op
	return nil
}

func requestGrantOperation(r *GrantRequest) grantOperation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.operation
}

func canonicalGrantNames(names []string) ([]string, error) {
	canonical := slices.Clone(names)
	sort.Strings(canonical)
	for i, name := range canonical {
		if !secretNameRE.MatchString(name) || name == "SSH_AUTH_SOCK" || i > 0 && canonical[i-1] == name {
			return nil, grantOperationError()
		}
	}
	return canonical, nil
}

func bindNewGrantIntent(ctx context.Context, h Host, hostID string, names []string, sshAgent bool) error {
	if err := requireGrantOperation(ctx, grantOpNew); err != nil {
		return err
	}
	canonical, err := canonicalGrantNames(names)
	if err != nil {
		return err
	}
	wall, exposed := creationGrantFacts(ctx)
	r := grantRequestOf(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if before := r.newIntent; before != nil {
		if !SameEntry(before.host, h) || before.hostID != hostID || !slices.Equal(before.names, canonical) ||
			before.sshAgent != sshAgent || before.wall != wall || !slices.Equal(before.exposed, exposed) {
			return grantOperationError()
		}
		return nil
	}
	r.newIntent = &newGrantIntent{host: cloneGrantHost(h), hostID: hostID, names: canonical, sshAgent: sshAgent,
		wall: wall, exposed: slices.Clone(exposed)}
	return nil
}

func bindLegacyResumeIntent(ctx context.Context, h Host, hostID, id string, names []string, p *SandboxPolicy) error {
	if err := requireGrantOperation(ctx, grantOpResume); err != nil {
		return err
	}
	canonical, err := canonicalGrantNames(names)
	if err != nil {
		return err
	}
	r := grantRequestOf(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resumeGroup || r.resume != nil || r.join != nil || p != nil && (p.FutureLineage || p.lineage != nil) {
		return grantOperationError()
	}
	if before := r.legacyResume; before != nil {
		if !SameEntry(before.host, h) || before.hostID != hostID || before.id != id ||
			!slices.Equal(before.names, canonical) || !sameSandboxCreation(before.creation, p) {
			return grantOperationError()
		}
		return nil
	}
	r.legacyResume = &legacyResumeGrantIntent{host: cloneGrantHost(h), hostID: hostID, id: id,
		names: canonical, creation: cloneSandboxPolicy(p)}
	return nil
}

func grantOperationError() error {
	return &UserError{Msg: "this request already belongs to another session operation or value selection",
		Hint: "Start a new logical request for the new operation, target or secret list.", Code: "grant_scope", Kind: KindRefused}
}

func newFetchAgentIntent(r *GrantRequest) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.SSHAgent || r.newIntent != nil && r.newIntent.sshAgent
}
