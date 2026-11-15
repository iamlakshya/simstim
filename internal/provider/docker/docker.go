// Package docker makes hosts that are local Docker containers, reached with
// `docker exec -i` instead of SSH. It is experimental: a test bed for the
// exec transport. A container has no systemd, so the helper runs sessions as
// detached processes without memory caps, and Docker's default seccomp
// profile stops agents' own sandboxes (bubblewrap).
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
func (Provider) Create(ctx context.Context, req provider.Request) (result client.Host, returned error) {
	scope, err := client.HostCreationSourceScopeContext(ctx)
	if err != nil {
		return client.Host{}, err
	}
	if scope.Request() != provider.CreationSourceRequest("docker", req) {
		return client.Host{}, sourceUnknown()
	}
	frame := &dockerSourceBirth{scope: scope, request: scope.Request()}
	ctx = context.WithValue(ctx, dockerSourceBirthKey{}, frame)
	frame.ctx = ctx
	defer func() {
		returned = errors.Join(returned, frame.metadata.Close())
		locator := ""
		if frame.locator != nil {
			locator = frame.locator.endpoint
			returned = errors.Join(returned, frame.locator.close())
		}
		if returned != nil && frame.attempted {
			if result.Machine == nil {
				result = freezeBirthHost(frame.partial)
			}
			returned = provider.RetainedCreation(result, frame.runID, locator, returned)
		}
	}()
	metadata, err := client.OpenProviderSourceMetadata(ctx)
	if err != nil {
		return client.Host{}, err
	}
	frame.metadata = metadata
	locator, err := captureSourceLocator(ctx)
	if err != nil {
		return client.Host{}, err
	}
	frame.locator = &locator
	if err := locator.checkInspector(ctx); err != nil {
		return client.Host{}, err
	}
	out := req.Out
	if out == nil {
		out = io.Discard
	}
	c := provider.MachineName(req.Name)
	rawState, err := locator.exec(ctx, "inspect", "-f", "{{.State.Status}}", c)
	st := strings.TrimSpace(string(rawState))
	if err != nil && (strings.Contains(strings.ToLower(err.Error()), "no such object") || strings.Contains(strings.ToLower(err.Error()), "no such container")) {
		st, err = "", nil
	}
	if err != nil {
		return client.Host{}, err
	}
	if st != "" {
		return client.Host{}, &client.UserError{Msg: "Docker already has a container called " + c, Hint: "Inspect it on the selected original Docker endpoint, or choose another name.", Code: "instance_exists"}
	}
	image := req.Image
	if image == "" {
		image = DefaultImage
	}
	args := RunArgs(c, req.Name, image)
	fmt.Fprintf(out, "  Starting the container: docker %s\n", strings.Join(args, " "))
	frame.partial = client.Host{Name: req.Name, Machine: &client.Machine{Provider: "docker", ID: c, User: DefaultUser, Image: image}}
	frame.attempted = true
	runID, err := locator.exec(ctx, args...)
	frame.runID = strings.TrimSpace(string(runID))
	if err != nil {
		return client.Host{}, err
	}
	user := DefaultUser
	if _, err := locator.exec(ctx, "exec", "-u", "0", strings.TrimSpace(string(runID)), "sh", "-c", addUser); err != nil {
		fmt.Fprintf(out, "  Note: could not add the user %q (%v), so sessions run as root.\n", DefaultUser, err)
		user = "root"
	}
	host := client.Host{Name: req.Name, Machine: &client.Machine{Provider: "docker", ID: c, User: user, Image: image}}
	source, err := locator.receipt(ctx, host, string(runID))
	if err != nil {
		return client.Host{}, &client.UserError{Code: "source_inventory", Msg: "the created container is retained because its actual source provenance could not be confirmed", Hint: "Inspect the container on its original Docker engine before creating another host.", Kind: client.KindRefused}
	}
	host.Machine.Source = source
	frame.result = freezeBirthHost(host)
	return client.RecordHostMachineSourceBirthContext(ctx, host)
}

// Open returns the lifecycle bound to the original engine and resource.
func (Provider) Open(h client.Host) (client.Lifecycle, error) {
	if h.Machine == nil || h.Machine.Provider != "docker" {
		return nil, sourceUnknown()
	}
	source, err := client.NormaliseMachineSource("docker", h.Machine.Source)
	if err != nil {
		return nil, err
	}
	if source.Config.Role == client.MachineConfigUnused {
		return nil, sourceUnknown()
	}
	user := h.Machine.User
	if user == "" {
		user = "root"
	}
	return &container{host: freezeBirthHost(h), source: source, user: user}, nil
}

type container struct {
	host   client.Host
	source *client.MachineSourceProvenance
	user   string
}

func state(s string) string {
	switch s {
	case "running":
		return client.MachineRunning
	case "restarting":
		return client.MachineStarting
	case "", "removing":
		return client.MachineMissing
	default:
		return client.MachineStopped
	}
}

// Transport runs commands in a container with `docker exec -i`: no TTY, so
// stdin and stdout are a clean byte stream. When the client goes, the Docker
// daemon kills the command it ran, and only that command.
type Transport struct {
	Container, User string
	saved           *container
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
	cmd, closeSource, err := t.command(ctx, remote)
	if err != nil {
		return nil, err
	}
	process, err := client.CommandProc(cmd, unreachable)
	if err != nil {
		return nil, errors.Join(err, closeSource())
	}
	return &dockerSourceProc{Proc: process, closeSource: closeSource}, nil
}

// Run runs remote in the container to its end and returns its stdout.
func (t *Transport) Run(ctx context.Context, remote string, stdin io.Reader) (_ []byte, returned error) {
	cmd, closeSource, err := t.command(ctx, remote)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, closeSource()) }()
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
	if t.saved != nil {
		source := t.saved.source
		return fmt.Sprintf("docker --host %s --config %s exec -it -u %s %s sh (check that this works first)",
			client.ShellWord(source.Endpoint), client.ShellWord(source.Config.Path), client.ShellWord(t.User), source.ResourceID)
	}
	return fmt.Sprintf("docker exec -it -u %s %s sh (check that this works first)", t.User, t.Container)
}
