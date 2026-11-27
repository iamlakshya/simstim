package client

import (
	"context"
	"reflect"
	"slices"
	"sync"

	"github.com/iamlakshya/simstim/internal/api"
)

// freshChildPlan is constructed only from the original local choice, retained
// settings, both local ancestors and one current protected cohort. Neither an
// inherited Join intent nor a host row can supply its selected local names.
type freshChildPlan struct {
	mu                                  sync.Mutex
	request                             *GrantRequest
	conn                                *Conn
	operation                           *settingsOperation
	choice, header                      api.JoinArgs
	selection                           *LocalAgentSelection
	ancestors                           *freshSandboxReservationSnapshot
	cohort                              *api.SandboxCohortView
	wall                                *wallGrantIntent
	intent                              api.SecretDeliveryIntent
	prospective, creation               *SandboxPolicy
	values                              map[string]api.Secret
	busy, fetched, attempted, published bool
	aborting, retired, closed           bool
}

// NewFreshChildGrantRequest keeps this fresh local agent choice separate from
// inherited joins, resumes and new sandboxes before any question is asked.
func NewFreshChildGrantRequest() *GrantRequest {
	return &GrantRequest{operation: grantOpFreshChild}
}

// PrepareFreshChild seals the original selection and reserves its OWN v2 local
// domain before any value command. Returned fields are data; only this SAME
// request's private plan can later fetch and publish the actual child.
func (c *Conn) PrepareFreshChild(ctx context.Context, choice api.JoinArgs) (api.JoinArgs, error) {
	choice.Argv = slices.Clone(choice.Argv)
	if c == nil || c.agentOnly {
		return choice, joinIntentError()
	}
	if err := requireGrantOperation(ctx, grantOpFreshChild); err != nil {
		return choice, err
	}
	r := grantRequestOf(ctx)
	if r.SSHAgent || r.Untrusted || requestWallIntent(ctx) != nil {
		return choice, joinIntentError()
	}
	if err := CheckSessionName(choice.Name); err != nil {
		return choice, err
	}
	if err := checkSessionID(c.Host.Name, choice.Parent); err != nil {
		return choice, err
	}
	if err := bindRequestTransportSource(ctx, c); err != nil {
		return choice, err
	}
	r.mu.Lock()
	plan := r.freshChild
	r.mu.Unlock()
	if plan == nil {
		if choice.ID != "" || choice.Snapshot != nil || choice.SecretGeneration != "" || choice.SecretDelivery != nil || len(choice.Secrets) != 0 {
			return choice, joinIntentError()
		}
		operation, err := r.borrowNewSettings(ctx)
		if err != nil {
			return choice, err
		}
		candidate, err := c.newFreshChildPlan(ctx, r, operation, choice)
		if err != nil {
			return choice, err
		}
		r.mu.Lock()
		if err := r.lifetimeErrorLocked(); err != nil {
			r.mu.Unlock()
			return choice, err
		}
		if r.freshChild == nil {
			r.freshChild = candidate
		}
		plan = r.freshChild
		r.mu.Unlock()
	}
	if err := plan.checkChoice(c, choice); err != nil {
		return choice, err
	}
	if err := plan.authorise(ctx); err != nil {
		return choice, err
	}
	if err := plan.revalidateCohort(ctx); err != nil {
		return choice, err
	}
	if err := plan.reserve(ctx); err != nil {
		return choice, err
	}
	return plan.arguments(ctx, choice)
}

func (p *freshChildPlan) checkChoice(c *Conn, a api.JoinArgs) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.aborting || p.retired || p.attempted || p.published || p.busy || p.conn != c ||
		a.Parent != p.choice.Parent || a.Name != p.choice.Name || a.Agent != p.choice.Agent || a.Task != p.choice.Task ||
		!slices.Equal(a.Argv, p.choice.Argv) || a.Cols != p.choice.Cols || a.Rows != p.choice.Rows {
		return joinIntentError()
	}
	for _, pair := range [][2]string{{a.Term, p.header.Term}, {a.ColorTerm, p.header.ColorTerm}, {a.TermProgram, p.header.TermProgram}, {a.TermProgramVersion, p.header.TermProgramVersion}} {
		if pair[0] != "" && pair[0] != pair[1] {
			return joinIntentError()
		}
	}
	if a.ID != "" && (p.creation == nil || a.ID != p.creation.SessionID) ||
		a.Snapshot != nil && !sameGrantSnapshot(a.Snapshot, p.cohort.Snapshot) ||
		a.SecretGeneration != "" && a.SecretGeneration != p.cohort.SecretState.Generation ||
		a.SecretDelivery != nil && !reflect.DeepEqual(*a.SecretDelivery, p.intent) ||
		len(a.Secrets) != 0 && (!p.fetched || !sameSelectedSecretValues(a.Secrets, p.values)) {
		return joinIntentError()
	}
	return nil
}

func (p *freshChildPlan) arguments(ctx context.Context, a api.JoinArgs) (api.JoinArgs, error) {
	if err := p.operation.Revalidate(ctx); err != nil {
		return a, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.aborting || p.retired || p.creation == nil {
		return a, joinIntentError()
	}
	a = p.header
	a.Argv = slices.Clone(p.header.Argv)
	a.ID = p.creation.SessionID
	a.Snapshot = p.cohort.Snapshot.Clone()
	a.SecretGeneration = p.cohort.SecretState.Generation
	intent := api.CloneSecretDeliveryIntent(p.intent)
	a.SecretDelivery = &intent
	return a, nil
}

func (p *freshChildPlan) close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy {
		return agentModeResourceError()
	}
	p.closed = true
	p.values = nil
	return nil
}
