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
	for {
		err := run(cmd.Context())
		var q *client.ConfirmError
		if !errors.As(err, &q) {
			return err
		}
		if err := answerGrant(cmd, err, q); err != nil {
			return err
		}
	}
}

func answerGrant(cmd *cobra.Command, original error, q *client.ConfirmError) error {
	p := prompts(cmd)
	if !p.on {
		return original
	}
	if q.Prompt == "" || !client.ValidHostName(q.Host) {
		return &client.UserError{Msg: "the grant question is not valid",
			Hint: "Run the command again.", Code: "grant_confirmation", Kind: client.KindRefused}
	}
	var yes bool
	var err error
	switch q.Kind {
	case client.ConfirmYesNo:
		question := qSharedNew
		if cmd.Name() == "resume" {
			question = qSharedResume
		}
		yes, err = p.confirm(question, client.CleanLine(q.Prompt), "a terminal answer for the grant")
	case client.ConfirmHostName:
		yes, err = p.typedName(client.CleanLine(q.Prompt), q.Host)
	default:
		return &client.UserError{Msg: "the grant question has an unknown answer type",
			Hint: "Run the command again.", Code: "grant_confirmation", Kind: client.KindRefused}
	}
	if err != nil {
		return err
	}
	if !yes {
		return p.cancel()
	}
	if stopped(p.ctx) {
		return errInterrupted
	}
	ctx, err := client.ConfirmGrant(cmd.Context(), original)
	if err != nil {
		return err
	}
	cmd.SetContext(ctx)
	return nil
}
