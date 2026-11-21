//go:build unix

package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCommandTerminalFilesPreserveOriginalIOWithoutEnablingAccess(t *testing.T) {
	input, err := os.OpenFile(filepath.Join(t.TempDir(), "input"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if _, err := input.WriteString("owned-terminal-input\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	diagnostic, err := os.OpenFile(filepath.Join(t.TempDir(), "diagnostic"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer diagnostic.Close()
	originalIn, originalOut, originalErr := os.Stdin, os.Stdout, os.Stderr
	ctx := WithCommandTerminalV1(context.Background(), input, diagnostic)
	value, err := runSecret(ctx, "OWNED", "read value; printf '%s\\n' \"$value\"; printf 'owned diagnostic\\n' >&2", true)
	if err != nil || string(value) != "owned-terminal-input" {
		t.Fatal("authorised command lost its actual terminal files", err)
	}
	written, err := os.ReadFile(diagnostic.Name())
	if err != nil || string(written) != "owned diagnostic\n" {
		t.Fatal("command diagnostic reached another terminal", string(written), err)
	}
	if _, err := input.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	value, err = runSecret(ctx, "UNAUTHORISED_TERMINAL", "if read value; then printf wrong; else printf no-terminal; fi", false)
	if err != nil || string(value) != "no-terminal" {
		t.Fatal("carried terminal files enabled terminal access", err)
	}
	if os.Stdin != originalIn || os.Stdout != originalOut || os.Stderr != originalErr {
		t.Fatal("terminal handoff swapped process-global files")
	}
}
