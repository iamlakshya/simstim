// Package docker makes hosts that are local Docker containers, reached with
// `docker exec -i` instead of SSH. It is experimental: a test bed for the
// exec transport. A container has no systemd, so the helper runs sessions as
// detached processes without memory caps, and Docker's default seccomp
// profile stops agents' own sandboxes (bubblewrap).
package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/iamlakshya/simstim/internal/brand"
	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

const install = "Install Docker (https://docs.docker.com/get-docker/), start it, then run this again."

// DefaultImage has bash and coreutils, which the helper's install needs.
const DefaultImage = "debian:stable-slim"

// DefaultUser runs the helper and the sessions, as a login user would on a VM.
const DefaultUser = "user"

// Provider makes Docker containers.
type Provider struct{}

// Name is "docker".
func (Provider) Name() string { return "docker" }

// RunArgs are the docker arguments that start the container: --init reaps
// detached sessions, and `sleep infinity` keeps it running.
func RunArgs(container, host, image string) []string {
	return []string{"run", "-d", "--init", "--name", container, "--label", brand.Name + ".host=" + host, image, "sleep", "infinity"}
}

// addUser makes DefaultUser with a home folder and bash, when the image can.
const addUser = `id -u ` + DefaultUser + ` >/dev/null 2>&1 || useradd -m -s /bin/bash ` + DefaultUser

// Create starts the container and gives it a user like a VM's.
func (Provider) Create(ctx context.Context, req provider.Request) (client.Host, error) {
	out := req.Out
	if out == nil {
		out = io.Discard
	}
	c := provider.MachineName(req.Name)
	st, err := inspect(ctx, c)
	if err != nil {
		return client.Host{}, err
	}
	if st != "" {
		return client.Host{}, &client.UserError{Msg: "Docker already has a container called " + c, Hint: "Pick another name, or remove the container with docker rm -f " + c + ".", Code: "instance_exists"}
	}
	image := req.Image
	if image == "" {
		image = DefaultImage
	}
	args := RunArgs(c, req.Name, image)
	fmt.Fprintf(out, "  Starting the container: docker %s\n", strings.Join(args, " "))
	if _, err := provider.Exec(ctx, install, "docker", args...); err != nil {
		return client.Host{}, err
	}
	user := DefaultUser
	if _, err := provider.Exec(ctx, install, "docker", "exec", "-u", "0", c, "sh", "-c", addUser); err != nil {
		fmt.Fprintf(out, "  Note: could not add the user %q (%v), so sessions run as root.\n", DefaultUser, err)
		user = "root"
	}
	return client.Host{Name: req.Name, Machine: &client.Machine{Provider: "docker", ID: c, User: user, Image: image}}, nil
}

// inspect returns the container's state, or "" when there is no container.
func inspect(ctx context.Context, c string) (string, error) {
	out, err := provider.Exec(ctx, install, "docker", "inspect", "-f", "{{.State.Status}}", c)
	if err != nil {
		if msg := strings.ToLower(err.Error()); strings.Contains(msg, "no such object") || strings.Contains(msg, "no such container") {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Open returns the lifecycle of a container this provider made.
func (Provider) Open(h client.Host) (client.Lifecycle, error) {
	user := h.Machine.User
	if user == "" {
		user = "root"
	}
	return &container{name: h.Machine.ID, user: user}, nil
}

type container struct {
	name, user string
}

// Status maps Docker's state to the client's machine states.
func (c *container) Status(ctx context.Context) (string, error) {
	st, err := inspect(ctx, c.name)
	if err != nil {
		return "", err
	}
	return state(st), nil
}

func state(s string) string {
	switch s {
	case "running":
		return client.MachineRunning
	case "restarting":
		return client.MachineStarting
	case "", "removing":
		return client.MachineMissing
	default: // created, exited, paused, dead
		return client.MachineStopped
	}
}

// Start starts or unpauses the container.
func (c *container) Start(ctx context.Context) error {
	st, err := inspect(ctx, c.name)
	if err != nil {
		return err
	}
	verb := "start"
	if st == "paused" {
		verb = "unpause"
	}
	_, err = provider.Exec(ctx, install, "docker", verb, c.name)
	return err
}

// Stop stops the container. Its files stay; its sessions end.
func (c *container) Stop(ctx context.Context) error {
	_, err := provider.Exec(ctx, install, "docker", "stop", "-t", "5", c.name)
	return err
}

// Destroy deletes the container and its files.
func (c *container) Destroy(ctx context.Context) error {
	st, err := inspect(ctx, c.name)
	if err != nil || st == "" {
		return err
	}
	_, err = provider.Exec(ctx, install, "docker", "rm", "-f", c.name)
	return err
}

// Transport is `docker exec -i` into the container.
func (c *container) Transport(context.Context, bool) (client.Transport, error) {
	return &Transport{Container: c.name, User: c.user}, nil
}

// Transport runs commands in a container with `docker exec -i`: no TTY, so
// stdin and stdout are a clean byte stream. When the client goes, the Docker
// daemon kills the command it ran, and only that command.
type Transport struct {
	Container, User string
}

func (t *Transport) args(remote string) []string {
	return []string{"exec", "-i", "-u", t.User, t.Container, "sh", "-c", remote}
}

// unreachable tells a failure of docker itself from a failure of the
// command in the container.
func unreachable(_ int, stderr string) bool {
	for _, s := range []string{"Error response from daemon", "No such container", "is not running", "Cannot connect to the Docker daemon", "error during connect"} {
		if strings.Contains(stderr, s) {
			return true
		}
	}
	return false
}

// Start runs remote in the container.
func (t *Transport) Start(ctx context.Context, remote string) (client.Proc, error) {
	cmd := exec.CommandContext(ctx, "docker", t.args(remote)...)
	return client.CommandProc(cmd, unreachable)
}

// Run runs remote in the container to its end and returns its stdout.
func (t *Transport) Run(ctx context.Context, remote string, stdin io.Reader) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", t.args(remote)...)
	var out, errb bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, &out, &errb
	if err := cmd.Run(); err != nil {
		msg := provider.Tail(errb.String(), 3)
		if unreachable(0, errb.String()) {
			return out.Bytes(), &client.UserError{Msg: fmt.Sprintf("cannot reach the container %s: %s", t.Container, msg), Hint: t.ReachHint(), Code: "unreachable"}
		}
		return out.Bytes(), fmt.Errorf("%w: %s", err, msg)
	}
	return out.Bytes(), nil
}

// ReachHint is the docker command that should work first.
func (t *Transport) ReachHint() string {
	return fmt.Sprintf("docker exec -it -u %s %s sh (check that this works first)", t.User, t.Container)
}
