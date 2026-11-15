package docker

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"sync"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

type dockerSourceOperation struct {
	owner       *container
	pin         *provider.SourceConfigPin
	metadata    *client.ProviderSourceMetadata
	environment []string
	status      string
	missing     bool
}

func (c *container) open(ctx context.Context) (_ *dockerSourceOperation, returned error) {
	operation := &dockerSourceOperation{owner: c}
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, operation.close())
		}
	}()
	var err error
	operation.pin, err = provider.PinSourceConfig(c.source.Config)
	if err != nil {
		return nil, errors.Join(sourceUnknown(), err)
	}
	operation.metadata, err = client.OpenProviderSourceMetadata(ctx)
	if err != nil {
		return nil, err
	}
	operation.environment, err = operation.metadata.Environment(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := operation.read(ctx, "info", "--format", "{{json .ID}}")
	var engine string
	if err != nil {
		var unavailable *client.UserError
		if errors.As(err, &unavailable) && unavailable.Code == "tool_missing" {
			return nil, err
		}
	}
	if err != nil || json.Unmarshal(raw, &engine) != nil || engine != c.source.EngineID {
		return nil, errors.Join(sourceUnknown(), err)
	}
	raw, err = operation.read(ctx, "container", "inspect", c.source.ResourceID, "--format", sharingTemplate())
	if err != nil {
		if missingResource(err) {
			operation.missing = true
			return operation, nil
		}
		return nil, errors.Join(sourceUnknown(), err)
	}
	var sharing dockerSharing
	if json.Unmarshal(raw, &sharing) != nil || !sharingMatches(sharing, c.host, c.source.ResourceID) {
		return nil, sourceUnknown()
	}
	raw, err = operation.read(ctx, "inspect", "-f", "{{.State.Status}}", c.source.ResourceID)
	if err != nil {
		return nil, errors.Join(sourceUnknown(), err)
	}
	operation.status = state(strings.TrimSpace(string(raw)))
	return operation, nil
}

func missingResource(err error) bool {
	var tool *provider.ExecError
	if !errors.As(err, &tool) {
		return false
	}
	message := strings.ToLower(tool.Stderr)
	return strings.Contains(message, "no such object") || strings.Contains(message, "no such container")
}

func (o *dockerSourceOperation) args(args ...string) []string {
	return append([]string{"--host", o.owner.source.Endpoint, "--config", o.owner.source.Config.Path}, args...)
}

func (o *dockerSourceOperation) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.pin.Revalidate(); err != nil {
		return errors.Join(sourceUnknown(), err)
	}
	return o.metadata.Revalidate(ctx)
}

func (o *dockerSourceOperation) read(ctx context.Context, args ...string) ([]byte, error) {
	if err := o.check(ctx); err != nil {
		return nil, err
	}
	data, err := provider.ExecMetadata(ctx, o.environment, 4<<20, install, "docker", o.args(args...)...)
	return data, errors.Join(err, o.check(ctx))
}

func (o *dockerSourceOperation) close() error {
	clear(o.environment)
	return errors.Join(o.pin.Close(), o.metadata.Close())
}

func goneResource() error {
	return &client.UserError{Code: "machine_gone", Msg: "the original Docker container is gone", Hint: "Forget the saved host, or inspect its original Docker engine before making a new host.", Kind: client.KindFailed}
}

// Status reads the original immutable container ID on its original engine.
func (c *container) Status(ctx context.Context) (_ string, returned error) {
	operation, err := c.open(ctx)
	if err != nil {
		return "", err
	}
	defer func() { returned = errors.Join(returned, operation.close()) }()
	if operation.missing {
		return client.MachineMissing, nil
	}
	return operation.status, nil
}

func (c *container) action(ctx context.Context, action string, args ...string) (returned error) {
	operation, err := c.open(ctx)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, operation.close()) }()
	if operation.missing {
		if action == "rm" {
			return nil
		}
		return goneResource()
	}
	if action == "start" && operation.status == client.MachineStopped {
		// Docker distinguishes a paused container from an exited one.
		raw, err := operation.read(ctx, "inspect", "-f", "{{.State.Status}}", c.source.ResourceID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(raw)) == "paused" {
			action = "unpause"
		}
	}
	command := append([]string{action}, args...)
	command = append(command, c.source.ResourceID)
	if err := operation.check(ctx); err != nil {
		return err
	}
	_, err = provider.ExecEnvironment(ctx, operation.environment, install, "docker", operation.args(command...)...)
	return errors.Join(err, operation.check(ctx))
}

// Start starts the original container, or unpauses that same resource.
func (c *container) Start(ctx context.Context) error { return c.action(ctx, "start") }

// Stop stops the original container. A reused name is never a target.
func (c *container) Stop(ctx context.Context) error { return c.action(ctx, "stop", "-t", "5") }

// Destroy deletes only the original container after checking engine and source.
func (c *container) Destroy(ctx context.Context) error { return c.action(ctx, "rm", "-f") }

// Transport retains the original source in each command's actual lifetime.
func (c *container) Transport(ctx context.Context, _ bool) (_ client.Transport, returned error) {
	operation, err := c.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, operation.close()) }()
	if operation.missing {
		return nil, goneResource()
	}
	return &Transport{Container: c.source.ResourceID, User: c.user, saved: c}, nil
}

func (t *Transport) command(ctx context.Context, remote string) (*exec.Cmd, func() error, error) {
	if t.saved == nil {
		// An explicit standalone transport retains its existing direct API.
		// Provider.Open never constructs this ambient branch.
		return exec.CommandContext(ctx, "docker", t.args(remote)...), func() error { return nil }, nil
	}
	operation, err := t.saved.open(ctx)
	if err != nil {
		return nil, nil, err
	}
	if operation.missing {
		return nil, nil, errors.Join(goneResource(), operation.close())
	}
	command, err := provider.CommandEnvironment(ctx, operation.environment, "docker", operation.args(t.args(remote)...)...)
	if err == nil {
		err = operation.check(ctx)
	}
	if err != nil {
		return nil, nil, errors.Join(err, operation.close())
	}
	return command, operation.close, nil
}

type dockerSourceProc struct {
	client.Proc
	once        sync.Once
	closeSource func() error
	waitErr     error
}

func (p *dockerSourceProc) Wait() error {
	p.once.Do(func() { p.waitErr = errors.Join(p.Proc.Wait(), p.closeSource()) })
	return p.waitErr
}
