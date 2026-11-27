package cli

import (
	"context"
	"os"
	"testing"
)

// mainLine runs the command line as Main does, and returns what it printed
// on stdout and stderr, and its exit code.
func mainLine(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	code := 0
	out, errOut := captureOutput(t, func() {
		root := Root()
		root.SetArgs(args)
		cmd, err := root.ExecuteContextC(withLine(context.Background(), args))
		asJSON := jsonAsked(cmd, args)
		warnOldFlags(cmd)
		code = finish(os.Stderr, err, cmd, colourAsked(cmd, args, os.Stderr), false, asJSON)
	})
	return out, errOut, code
}
