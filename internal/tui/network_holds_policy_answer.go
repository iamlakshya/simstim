package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/iamlakshya/simstim/internal/api"
	"github.com/iamlakshya/simstim/internal/client"
)

func (m *Model) answerPolicyHold(d *policySheet, scope api.NetworkHoldScope) tea.Cmd {
	request, ok := d.selectedHold()
	if !ok {
		return nil
	}
	if request.Reason == api.NetworkHoldUploadBudget && scope != api.NetworkHoldOnce {
		d.err = heldUploadScopeErrorV1()
		return nil
	}
	args := api.NetworkHoldActionArgs{Version: api.NetworkHoldV1, Session: d.id, Request: request.ID, Scope: scope}
	return m.changePolicyHold(d, func(ctx context.Context, connection policyConn) error {
		c, ok := connection.(interface {
			AnswerNetworkRequest(context.Context, api.NetworkHoldActionArgs) (api.NetworkHoldAnswerResult, error)
		})
		if !ok {
			return heldPolicyUnavailable()
		}
		var selected client.FileLayer
		if scope == api.NetworkHoldRepo || scope == api.NetworkHoldProfile {
			selector, ok := connection.(interface {
				NetworkHoldFileLayerV1(context.Context, string, api.NetworkHoldScope) (client.FileLayer, error)
			})
			if !ok {
				return heldPolicyUnavailable()
			}
			var err error
			selected, err = selector.NetworkHoldFileLayerV1(ctx, d.id, scope)
			if err != nil {
				return err
			}
		}
		request, err := client.NewNetworkHoldActionRequestV1(args, selected)
		if err != nil {
			return err
		}
		ordinary := ctx
		if scope == api.NetworkHoldRepo || scope == api.NetworkHoldProfile {
			change, err := client.BeginPolicyChange(ctx)
			if err != nil {
				return err
			}
			defer change.Close()
			ordinary = change.Context(ordinary)
		}
		ctx = client.WithGrantRequest(ordinary, request)
		err = m.grantQuestions(ctx, func(ctx context.Context) error {
			if scope == api.NetworkHoldRepo || scope == api.NetworkHoldProfile {
				publisher, ok := connection.(interface {
					PublishNetworkRequest(context.Context, api.NetworkHoldActionArgs, client.FileLayer) (client.FilePolicyResult, error)
				})
				if !ok {
					return heldPolicyUnavailable()
				}
				_, err := publisher.PublishNetworkRequest(ctx, args, selected)
				return err
			}
			_, err := c.AnswerNetworkRequest(ctx, args)
			return err
		})
		if err = errors.Join(err, request.Close()); err != nil {
			return err
		}
		if scope == api.NetworkHoldRepo || scope == api.NetworkHoldProfile {
			return m.propagateHeldFilePolicyV1(ordinary, selected)
		}
		return nil
	}, "Answered the held network request.")
}

func (m *Model) propagateHeldFilePolicyV1(ctx context.Context, selected client.FileLayer) error {
	plan, err := client.PlanPolicyPropagation(ctx, &selected)
	if err != nil {
		return err
	}
	defer plan.Close()
	for _, application := range plan.Applications() {
		if !application.Replacement.Preview().Changed {
			continue
		}
		if err := m.grantQuestions(application.Replacement.Context(ctx), application.Replacement.Check); err != nil {
			plan.Block(application.Host, err)
		}
	}
	result, err := plan.Apply(ctx)
	if err != nil {
		return err
	}
	if len(result.Unreached) != 0 {
		return &client.UserError{Msg: "the wider rule was saved, but some hosts still have a pending policy change",
			Hint: "Run the policy apply command to review and apply the pending change.", Code: "policy_pending", Kind: client.KindFailed}
	}
	return nil
}

func heldUploadScopeErrorV1() error {
	return &client.UserError{Msg: "an upload budget hold supports only once or refuse",
		Hint: "Change network.upload_budget in the selected policy layer with policy edit, then apply that policy.", Code: "upload_budget_scope", Kind: client.KindRefused}
}
