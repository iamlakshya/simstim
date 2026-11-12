// Package provider makes hosts: the VMs, containers and sandboxes that `host
// create` starts, bootstraps and connects, and `host destroy` deletes. Each
// provider is a sub-package (lima, docker, e2b). The client reaches their
// machines through the lifecycle and transport they return. The design is in
// docs/contributing/decisions/0013-host-providers.md.
package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
)

// Provider makes machines for new hosts, and opens the lifecycle of the
// machines it made. With the Lifecycle it returns, it covers create, status,
// start or resume, stop or pause, destroy, and the transport.
type Provider interface {
	// Name is the word for the provider in `host create <name>` and in
	// hosts.toml.
	Name() string
	// Create makes a machine for a new host and returns the host to save,
	// with its machine recorded. Progress lines go to req.Out.
	Create(ctx context.Context, req Request) (client.Host, error)
	// Open returns the lifecycle of a machine this provider made.
	Open(h client.Host) (client.Lifecycle, error)
}

// Request describes the machine to make for a new host.
type Request struct {
	// Name is the new host's name.
	Name string
	// CPUs, MemoryGiB and DiskGiB size a VM.
	CPUs, MemoryGiB, DiskGiB int
	// Image is the container image, for docker.
	Image string
	// Template is the sandbox template, for e2b.
	Template string
	// IdleStop is how long the host may idle before it stops ("" for the
	// provider's default, or "never").
	IdleStop string
	// TokenCommand prints an API key, for providers that need one.
	TokenCommand string
	// Out gets progress lines.
	Out io.Writer
}

// MachineName is the provider-side name of a host's machine: the product
// name, a dash and the host's name. It marks what the product made.
func MachineName(host string) string { return brand.Name + "-" + host }

// Exec runs a provider's command-line tool (limactl, docker) and returns its
// stdout. A failure carries the end of its stderr. A missing tool is a user
// error that says where to get it.
func Exec(ctx context.Context, install string, name string, args ...string) ([]byte, error) {
	return ExecInput(ctx, nil, install, name, args...)
}

// ExecInput is Exec with stdin.
func ExecInput(ctx context.Context, stdin io.Reader, install string, name string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, &client.UserError{Msg: name + " is not installed, or not on your PATH", Hint: install, Code: "tool_missing"}
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return out.Bytes(), fmt.Errorf("%s %s: %w", name, firstWord(args), ctx.Err())
		}
		return out.Bytes(), &ExecError{
			Msg:    fmt.Sprintf("%s %s: %v: %s", name, firstWord(args), err, Tail(errb.String(), 3)),
			Stderr: errb.String(),
			Err:    err,
		}
	}
	return out.Bytes(), nil
}

// ExecError is a provider tool that failed. Its message ends with the last
// lines of the tool's stderr, cleaned by Tail. Stderr keeps all of it, as
// the tool wrote it, for a caller that looks for a known message. A caller
// that shows Stderr cleans it first.
type ExecError struct {
	Msg    string
	Stderr string
	Err    error
}

func (e *ExecError) Error() string { return e.Msg }

// Unwrap returns the error of the run, such as an *exec.ExitError.
func (e *ExecError) Unwrap() error { return e.Err }

// Tail is the last n non-empty lines of s, joined by "; ". s is what a
// command wrote to stderr, often a host's own text, and errors show it: so
// each line is cleaned (client.CleanLine).
func Tail(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimSpace(client.CleanLine(l)); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}

func firstWord(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// ErrExists is returned by Create when the provider already has a machine
// with the new host's name.
var ErrExists = errors.New("a machine with that name already exists")
