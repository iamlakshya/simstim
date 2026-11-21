package client

import (
	"context"
	"os"
)

type commandTerminalKeyV1 struct{}
type commandTerminalV1 struct{ input, diagnostic *os.File }

// WithCommandTerminalV1 carries the actual restored terminal files for an
// already-authorised interactive value command. It grants no terminal use;
// callers must still pass the existing Terminal decision and grant checks.
func WithCommandTerminalV1(ctx context.Context, input, diagnostic *os.File) context.Context {
	return context.WithValue(ctx, commandTerminalKeyV1{}, commandTerminalV1{input, diagnostic})
}

func commandTerminalFilesV1(ctx context.Context) (*os.File, *os.File) {
	if files, ok := ctx.Value(commandTerminalKeyV1{}).(commandTerminalV1); ok {
		return files.input, files.diagnostic
	}
	return os.Stdin, os.Stderr
}
