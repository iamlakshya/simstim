package lima

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamlakshya/simstim/internal/client"
	"github.com/iamlakshya/simstim/internal/provider"
)

type limaSourceLocator struct {
	source       client.MachineConfigSource
	pin, created *provider.SourceConfigPin
	environment  []string
}

func captureLimaSourceLocator() (_ *limaSourceLocator, err error) {
	selected := os.Getenv("LIMA_HOME")
	if selected == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		selected = filepath.Join(home, ".lima")
	}
	source, pin, err := provider.CaptureSourceConfigPinned(selected)
	if err != nil {
		return nil, err
	}
	locator := &limaSourceLocator{source: source, pin: pin}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, "LIMA_HOME") {
			locator.environment = append(locator.environment, entry)
		}
	}
	locator.environment = append(locator.environment, "LIMA_HOME="+source.Path)
	return locator, nil
}

func (l *limaSourceLocator) check(ctx context.Context, after bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l == nil || l.pin == nil {
		return plainSourceUnknown()
	}
	if l.source.Role == client.MachineConfigExisting {
		return l.pin.Revalidate()
	}
	if err := l.pin.RevalidateParent(); err != nil {
		return err
	}
	if l.created != nil {
		return l.created.Revalidate()
	}
	if err := l.pin.Revalidate(); err == nil {
		return nil
	}
	if !after {
		return plainSourceUnknown()
	}
	_, created, err := l.pin.ObserveCreatedDirectory()
	if err != nil {
		return err
	}
	l.created = created
	return l.created.Revalidate()
}

func (l *limaSourceLocator) close() error {
	if l == nil {
		return nil
	}
	clear(l.environment)
	return errors.Join(l.pin.Close(), l.created.Close())
}

// limaExec uses the original native locator for EVERY command in the private
// creation frame. Ordinary saved lifecycle calls keep their existing path.
func limaExec(ctx context.Context, args ...string) ([]byte, error) {
	frame, _ := ctx.Value(limaSourceBirthKey{}).(*limaSourceBirth)
	if frame == nil {
		return provider.Exec(ctx, install, "limactl", args...)
	}
	if frame.ctx != ctx || frame.locator == nil {
		return nil, plainSourceUnknown()
	}
	if err := frame.scope.CheckContext(ctx); err != nil {
		return nil, err
	}
	if err := frame.locator.check(ctx, false); err != nil {
		return nil, err
	}
	if frame.instancePin != nil {
		if err := frame.instancePin.Revalidate(); err != nil {
			return nil, err
		}
	}
	data, err := provider.ExecEnvironment(ctx, frame.locator.environment, install, "limactl", args...)
	if err != nil {
		return data, err
	}
	if err := frame.locator.check(ctx, true); err != nil {
		return nil, err
	}
	if frame.instancePin != nil {
		if err := frame.instancePin.Revalidate(); err != nil {
			return nil, err
		}
	}
	return data, frame.scope.CheckContext(ctx)
}
