package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"

	"github.com/iamlakshya/simstim/internal/jail"
)

func TestOperationalExitClassificationPreservesActualCleanupFailures(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "exit 7")
	operational := command.Run()
	if operational == nil {
		t.Fatal("native test command did not produce its actual exit status")
	}
	if err := withoutOperationalExitV1(fmt.Errorf("original run: %w", operational)); err != nil {
		t.Fatal("actual process exit became a cleanup failure", err)
	}
	cleanup := errors.New("original runtime directory sync failed")
	joined := errors.Join(operational, cleanup, &jail.ExitError{Code: 137})
	if remaining := withoutOperationalExitV1(joined); !errors.Is(remaining, cleanup) {
		t.Fatal("operational status hid joined original cleanup failure", remaining)
	}
	if remaining := withoutOperationalExitV1(errors.Join(operational, context.Canceled)); !errors.Is(remaining, context.Canceled) {
		t.Fatal("generic cancelled cleanup was silently discarded", remaining)
	}
	if err := withoutOperationalExitV1(&exec.ExitError{}); err == nil {
		t.Fatal("an empty exit DTO replaced an actual process status")
	}
}
