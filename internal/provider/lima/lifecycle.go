package lima

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

type limaSavedSourceKey struct{}
type limaSavedSource struct {
	mu          sync.Mutex
	closed      bool
	ctx         context.Context
	owner       *vm
	pin         *provider.SourceConfigPin
	metadata    *client.ProviderSourceMetadata
	environment []string
	missing     bool
}

func (v *vm) open(ctx context.Context) (_ *limaSavedSource, returned error) {
	operation := &limaSavedSource{owner: v}
	defer func() {
		if returned != nil {
			returned = errors.Join(returned, operation.Close())
		}
	}()
	var err error
	operation.pin, err = provider.PinSourceConfig(v.source.Config)
	if errors.Is(err, os.ErrNotExist) {
		operation.missing = true
		operation.ctx = context.WithValue(ctx, limaSavedSourceKey{}, operation)
		return operation, nil
	}
	if err != nil {
		return nil, errors.Join(plainSourceUnknown(), err)
	}
	operation.metadata, err = client.OpenProviderSourceMetadata(ctx)
	if err != nil {
		return nil, err
	}
	if err := operation.metadata.ReserveLimaHome(ctx); err != nil {
		return nil, err
	}
	operation.environment, err = operation.metadata.Environment(ctx)
	if err != nil {
		return nil, err
	}
	operation.environment = append(operation.environment, "LIMA_HOME="+filepath.Dir(v.source.Config.Path))
	operation.ctx = context.WithValue(ctx, limaSavedSourceKey{}, operation)
	return operation, nil
}

func (o *limaSavedSource) check(ctx context.Context) error {
	if o == nil || o.ctx != ctx || o.owner == nil || o.missing {
		return plainSourceUnknown()
	}
	return o.Revalidate(ctx)
}

// Revalidate is an additive SSH restriction over this SAME original native
// instance and settings source. It grants no host identity or send permission.
func (o *limaSavedSource) Revalidate(ctx context.Context) error {
	if o == nil || ctx == nil {
		return plainSourceUnknown()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.missing || o.ctx == nil || o.pin == nil || o.metadata == nil {
		return plainSourceUnknown()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.ctx.Err(); err != nil {
		return err
	}
	if err := o.pin.Revalidate(); err != nil {
		return errors.Join(plainSourceUnknown(), err)
	}
	return o.metadata.Revalidate(ctx)
}

func (o *limaSavedSource) exec(ctx context.Context, args ...string) ([]byte, error) {
	if err := o.check(ctx); err != nil {
		return nil, err
	}
	data, err := provider.ExecEnvironment(ctx, o.environment, install, "limactl", args...)
	if len(args) > 0 && args[0] == "delete" {
		// The original directory can be absent after this exact delete. Its
		// native handle stays retained until the command and operation join.
		return data, errors.Join(err, o.metadata.Revalidate(ctx), ctx.Err())
	}
	return data, errors.Join(err, o.check(ctx))
}

// Close joins closure of this operation's original native and settings pins.
func (o *limaSavedSource) Close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	clear(o.environment)
	return errors.Join(o.pin.Close(), o.metadata.Close())
}

// OpenSSHNativeSource holds the original instance across the actual SSH
// configuration/key reads, start and wait owned by the client SSH producer.
func (v *vm) OpenSSHNativeSource(ctx context.Context) (client.SSHNativeSourceLease, error) {
	operation, err := v.open(ctx)
	if err != nil {
		return nil, err
	}
	if operation.missing {
		return nil, errors.Join(originalLimaGone(), operation.Close())
	}
	instance, err := find(operation.ctx, v.inst)
	if err != nil || instance == nil {
		if err == nil {
			err = originalLimaGone()
		}
		return nil, errors.Join(err, operation.Close())
	}
	return operation, nil
}

func originalLimaGone() error {
	return &client.UserError{Code: "machine_gone", Msg: "the original Lima instance is gone", Hint: "Forget the saved host, or inspect its original Lima folder before making a new host.", Kind: client.KindFailed}
}

// Status checks the original instance object and reads only its original home.
func (v *vm) Status(ctx context.Context) (_ string, returned error) {
	operation, err := v.open(ctx)
	if err != nil {
		return "", err
	}
	defer func() { returned = errors.Join(returned, operation.Close()) }()
	if operation.missing {
		return client.MachineMissing, nil
	}
	in, err := find(operation.ctx, v.inst)
	if err != nil {
		return "", err
	}
	if in == nil {
		return client.MachineMissing, nil
	}
	return state(in.Status), nil
}

func (v *vm) action(ctx context.Context, action string, args ...string) (returned error) {
	operation, err := v.open(ctx)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, operation.Close()) }()
	if operation.missing {
		if action == "delete" {
			return nil
		}
		return originalLimaGone()
	}
	in, err := find(operation.ctx, v.inst)
	if err != nil {
		return err
	}
	if in == nil {
		if action == "delete" {
			return nil
		}
		return originalLimaGone()
	}
	command := append([]string{action}, args...)
	command = append(command, v.inst)
	_, err = limaExec(operation.ctx, command...)
	return err
}

// Start starts the original instance under its original LIMA_HOME.
func (v *vm) Start(ctx context.Context) error { return v.action(ctx, "start", "--tty=false") }

// Stop stops the original instance under its original LIMA_HOME.
func (v *vm) Stop(ctx context.Context) error { return v.action(ctx, "stop") }

// Destroy deletes the checked original instance without a current-home lookup.
func (v *vm) Destroy(ctx context.Context) error { return v.action(ctx, "delete", "--force") }

// Transport resolves only the original instance's generated SSH configuration.
func (v *vm) Transport(ctx context.Context, interactive bool) (_ client.Transport, returned error) {
	operation, err := v.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { returned = errors.Join(returned, operation.Close()) }()
	if operation.missing {
		return nil, originalLimaGone()
	}
	in, err := find(operation.ctx, v.inst)
	if err != nil {
		return nil, err
	}
	if in == nil {
		return nil, originalLimaGone()
	}
	h := v.h
	h.SSH = sshAlias(v.inst)
	h.SSHConfig = filepath.Join(v.source.Config.Path, "ssh.config")
	return (&client.SSH{Host: h, Interactive: interactive}).WithNativeSource(v)
}
