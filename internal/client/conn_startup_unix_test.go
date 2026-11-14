//go:build !windows

package client

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

func TestDialCollectsRealExitAfterStdoutCloses(t *testing.T) {
	// read is a shell builtin. No child can inherit stderr and outlive the
	// command. The helper exits only once the client closes its stdin.
	cmd := exec.CommandContext(context.Background(), "sh", "-c", `
exec 1>&-
while IFS= read -r line; do :; done
printf 'the helper: not found\n' >&2
exit 127
`)
	p, err := CommandProc(cmd, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stdin().Close(); p.Kill(); _ = p.Wait() })
	_, err = dial(context.Background(), Host{Name: "box"}, route{t: startupTransport{proc: p}})
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("startup = %v, want missing helper with the real exit status", err)
	}
}
