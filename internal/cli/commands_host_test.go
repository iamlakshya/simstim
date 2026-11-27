package cli

import (
	"context"
	"io"
	"strings"
	"testing"
)

// said runs fn and returns what it said on stderr. It prints no data, so
// stdout must stay empty.
func said(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	var err error
	out, errOut := captureOutput(t, func() { err = fn() })
	if out != "" {
		t.Errorf("stdout got %q, which is no data", out)
	}
	return errOut, err
}

// quietPrompts is a prompter with input off, which asks nothing.
func quietPrompts(ctx context.Context) *prompter {
	return &prompter{ctx: ctx, in: strings.NewReader(""), out: io.Discard, path: "<name> test"}
}
