package cli

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/iamlakshya/simstim/internal/client"
)

type newStartCleanupKey struct{}

// newStartCleanup retains this synchronous start's actual last attempt budget.
// Terminal questions may retry the same request. Cleanup cannot extend a
// completed attempt's original absolute deadline or replace its source.
type newStartCleanup struct {
	request *client.GrantRequest
	budget  context.Context
	once    sync.Once
	err     error
}

func (c *newStartCleanup) finish() error {
	c.once.Do(func() {
		budget := c.budget
		if deadline, ok := budget.Deadline(); ok {
			var cancel context.CancelFunc
			budget, cancel = context.WithDeadline(context.WithoutCancel(budget), deadline)
			defer cancel()
		}
		_, abortErr := c.request.Abort(budget)
		c.err = errors.Join(abortErr, c.request.Close())
	})
	return c.err
}

func recordNewStartBudget(ctx context.Context, budget context.Context) {
	if owner, _ := ctx.Value(newStartCleanupKey{}).(*newStartCleanup); owner != nil {
		owner.budget = budget
	}
}

func newStartStep(cmd *cobra.Command, run func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
	defer cancel()
	recordNewStartBudget(cmd.Context(), ctx)
	return run(ctx)
}
