package client

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/iamlakshya/simstim/internal/brand"
)

// settingsOperation owns real decoded Settings and the SAME held source.
// Failed opens/decodes never return a successful operation or partial settings.
type settingsOperation struct {
	mu       sync.Mutex
	ctx      context.Context
	snapshot *agentSettingsSnapshot
	settings Settings
	closed   bool
}

type settingsReadFailure struct {
	err     error
	warning string
}

func (e *settingsReadFailure) Error() string { return e.err.Error() }
func (e *settingsReadFailure) Unwrap() error { return e.err }

func openSettingsOperation(ctx context.Context) (_ *settingsOperation, err error) {
	if ctx == nil {
		return nil, settingsOperationFailure(errAgentSettingsChanged)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot, err := openAgentSettingsSnapshot(ctx)
	if err != nil {
		return nil, settingsOperationFailure(err)
	}
	keep := false
	defer func() {
		if !keep {
			err = errors.Join(err, snapshot.close())
		}
	}()
	var settings Settings
	if !snapshot.absent {
		data, readErr := readOpenConfigFile(settingsPath(), snapshot.file)
		if readErr != nil {
			return nil, settingsOperationFailure(readErr)
		}
		if stopped := ctx.Err(); stopped != nil {
			return nil, stopped
		}
		if _, decodeErr := decodeConfig(string(data), &settings); decodeErr != nil {
			warning := "config.toml has an error, so its settings are not used"
			if _, line, _ := TOMLWhere(string(data), decodeErr); line > 0 {
				warning = fmt.Sprintf("config.toml has an error on line %d, so its settings are not used", line)
			}
			var alias *configAliasError
			if errors.As(decodeErr, &alias) {
				warning = "config.toml has an error: " + alias.Error() + ", so its settings are not used"
			}
			return nil, &settingsReadFailure{err: tomlError(settingsPath(), string(data), decodeErr), warning: warning}
		}
	}
	if err := snapshot.check(); err != nil {
		return nil, settingsOperationFailure(err)
	}
	keep = true
	return &settingsOperation{ctx: ctx, snapshot: snapshot, settings: withDefaults(settings)}, nil
}

func settingsOperationFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var prior *settingsReadFailure
	if errors.As(err, &prior) {
		return err
	}
	warning := "config.toml cannot be read, so its settings are not used"
	var user *UserError
	if errors.As(err, &user) {
		return &settingsReadFailure{err: err, warning: warning}
	}
	path := settingsPath()
	switch {
	case errors.Is(err, ErrConfigTooLarge):
		return &settingsReadFailure{err: &UserError{Msg: path + " cannot be used: " + err.Error(), Hint: brand.Name + " config check", Code: "bad_config", Kind: KindUsage}, warning: "config.toml is larger than 1 MiB, so its settings are not used"}
	case errors.Is(err, ErrNotAFile):
		return &settingsReadFailure{err: &UserError{Msg: path + " is not a regular file", Hint: "Check " + path + ".", Code: "bad_config", Kind: KindUsage}, warning: warning}
	default:
		return &settingsReadFailure{err: &UserError{Msg: "config.toml cannot be read", Hint: "Check " + path + ".", Code: "config_read", Kind: KindFailed}, warning: warning}
	}
}

// UI defaults carry the original failure. They are never a successful source
// operation and every canonical names/secret/grant helper preserves loadErr.
func failedSettingsRead(err error) (Settings, string) {
	warning := "config.toml cannot be read, so its settings are not used"
	var failure *settingsReadFailure
	if errors.As(err, &failure) {
		warning = failure.warning
	}
	return withDefaults(Settings{loadErr: err}), warning
}

func (o *settingsOperation) checkLocked(ctx context.Context) error {
	if o == nil || o.closed || o.snapshot == nil || ctx == nil {
		return errAgentSettingsChanged
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := o.ctx.Err(); err != nil {
		return err
	}
	return o.snapshot.check()
}

// Settings borrows the operation's actual decoded values. Callers must not
// mutate them or use them after the operation closes.
func (o *settingsOperation) Settings(ctx context.Context) (Settings, error) {
	if o == nil {
		return Settings{}, errAgentSettingsChanged
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.checkLocked(ctx); err != nil {
		return Settings{}, err
	}
	return o.settings, nil
}

// Snapshot returns the SAME physical source for the original plan/inspection.
func (o *settingsOperation) Snapshot(ctx context.Context) (*agentSettingsSnapshot, error) {
	if o == nil {
		return nil, errAgentSettingsChanged
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.checkLocked(ctx); err != nil {
		return nil, err
	}
	return o.snapshot, nil
}
func (o *settingsOperation) Revalidate(ctx context.Context) error {
	if o == nil {
		return errAgentSettingsChanged
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.checkLocked(ctx)
}
func (o *settingsOperation) Close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	err := o.snapshot.close()
	o.snapshot = nil
	o.settings = Settings{}
	o.ctx = nil
	return err
}
