package cli

import (
	"context"
	"errors"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
)

// grantQuestions retries a local gate after each live terminal answer.
// No flag supplies this approval, including --yes and --secret.
func grantQuestions(cmd *cobra.Command, run func(context.Context) error) error {
	next, err := grantQuestionsOn(cmd, nil, cmd.Context(), run)
	if err == nil {
		cmd.SetContext(next)
	}
	return err
}

// answerGrant keeps the original command context for callers that perform
// one explicit retry themselves. Phase orchestration uses answerGrantOn.
func answerGrant(cmd *cobra.Command, original error, q *client.ConfirmError) error {
	next, err := answerGrantOn(cmd, nil, cmd.Context(), original, q)
	if err == nil {
		cmd.SetContext(next)
	}
	return err
}

// grantQuestionsOn runs a gated phase closure on its own context without
// leaking that phase's GrantRequest onto cmd.Context(). When p is provided,
// any cancellation preserves p's prior change log.
func grantQuestionsOn(cmd *cobra.Command, p *prompter, ctx context.Context, run func(context.Context) error) (context.Context, error) {
	cur := ctx
	for {
		err := run(cur)
		var q *client.ConfirmError
		if !errors.As(err, &q) {
			return cur, err
		}
		var aerr error
		cur, aerr = answerGrantOn(cmd, p, cur, err, q)
		if aerr != nil {
			return cur, aerr
		}
	}
}

func answerGrantOn(cmd *cobra.Command, p *prompter, ctx context.Context, original error, q *client.ConfirmError) (context.Context, error) {
	if p == nil {
		p = prompts(cmd)
	}
	if !p.on {
		return ctx, original
	}
	if q.Prompt == "" || !client.ValidHostName(q.Host) {
		return ctx, &client.UserError{Msg: "the grant question is not valid",
			Hint: "Run the command again.", Code: "grant_confirmation", Kind: client.KindRefused}
	}
	var yes bool
	var err error
	switch q.Kind {
	case client.ConfirmYesNo:
		question := qSharedNew
		if cmd != nil && cmd.Name() == "resume" {
			question = qSharedResume
		}
		if cmd != nil && cmd.Name() == "rewind" {
			question = qSharedRewind
		}
		yes, err = p.confirm(question, client.CleanLine(q.Prompt), "a terminal answer for the grant")
	case client.ConfirmHostName:
		yes, err = p.typedName(client.CleanLine(q.Prompt), q.Host)
	default:
		return ctx, &client.UserError{Msg: "the grant question has an unknown answer type",
			Hint: "Run the command again.", Code: "grant_confirmation", Kind: client.KindRefused}
	}
	if err != nil {
		return ctx, err
	}
	if !yes {
		return ctx, p.cancel()
	}
	if stopped(p.ctx) {
		return ctx, errInterrupted
	}
	return client.ConfirmGrant(ctx, original)
}
