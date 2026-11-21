package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
)

// The launcher inherits this runner and uses it only after its terminal
// handoff. Both paths ask through the existing scoped terminal questions.
func configureAttachArmQuestionsV1(cmd *cobra.Command) {
	ctx := client.WithAttachArmQuestionsV1(cmd.Context(), func(ctx context.Context, attempt *client.AttachArmAttemptV1) error {
		original := cmd.Context()
		cmd.SetContext(attempt.Context(ctx))
		defer cmd.SetContext(original)
		return grantQuestions(cmd, attempt.Run)
	})
	cmd.SetContext(ctx)
}
